// SMTP email notification adapter (JSON-RPC over stdio).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"strings"
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
		_ = enc.Encode(handle(req))
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
}

func handle(req request) response {
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
		if err := sendMail(p.Recipient, p.Subject, p.Body); err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true}, ID: req.ID}
	default:
		return errResp(req.ID, -32601, "method not found: "+req.Method)
	}
}

func sendMail(to, subject, body string) error {
	host := envOr("SMTP_HOST", "localhost")
	port := envOr("SMTP_PORT", "25")
	from := envOr("SMTP_FROM", "gorchestrator@localhost")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASSWORD")
	addr := net.JoinHostPort(host, port)

	msg := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func errResp(id *int64, code int, msg string) response {
	return response{JSONRPC: "2.0", Error: &rpcError{Code: code, Message: msg}, ID: id}
}
