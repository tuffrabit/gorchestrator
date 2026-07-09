// Slack webhook notification adapter (JSON-RPC over stdio).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      *int64          `json:"id"`
}

type response struct {
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
	ID      *int64    `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	webhook := os.Getenv("SLACK_WEBHOOK_URL")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		resp := handle(req, webhook)
		_ = enc.Encode(resp)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
}

func handle(req request, webhook string) response {
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true, "port": "notification"}, ID: req.ID}
	case "notification.send":
		var p struct {
			Kind      string `json:"kind"`
			Recipient string `json:"recipient"`
			Subject   string `json:"subject"`
			Body      string `json:"body"`
			IssueID   int64  `json:"issue_id"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, err.Error())
		}
		if webhook == "" {
			return errResp(req.ID, -32000, "SLACK_WEBHOOK_URL not set")
		}
		text := p.Subject
		if p.Body != "" {
			text = p.Subject + "\n" + p.Body
		}
		payload, _ := json.Marshal(map[string]string{"text": text})
		client := &http.Client{Timeout: 15 * time.Second}
		httpResp, err := client.Post(webhook, "application/json", bytes.NewReader(payload))
		if err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		defer httpResp.Body.Close()
		if httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			return errResp(req.ID, -32000, fmt.Sprintf("slack status %d: %s", httpResp.StatusCode, strings.TrimSpace(string(b))))
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true}, ID: req.ID}
	default:
		return errResp(req.ID, -32601, "method not found: "+req.Method)
	}
}

func errResp(id *int64, code int, msg string) response {
	return response{JSONRPC: "2.0", Error: &rpcError{Code: code, Message: msg}, ID: id}
}
