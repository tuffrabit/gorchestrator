// GitHub Issues trigger adapter (JSON-RPC over stdio).
// Polls open issues with a label and emits trigger.issue notifications.
// Auth: GITHUB_TOKEN. Config via env: GITHUB_OWNER, GITHUB_REPO, GITHUB_LABEL (default gorch),
// GITHUB_PROJECT (required gorchestrator project name), GITHUB_POLL_SECONDS (default 60).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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
	token := os.Getenv("GITHUB_TOKEN")
	owner := os.Getenv("GITHUB_OWNER")
	repo := os.Getenv("GITHUB_REPO")
	label := os.Getenv("GITHUB_LABEL")
	if label == "" {
		label = "gorch"
	}
	project := os.Getenv("GITHUB_PROJECT")
	pollSec, _ := strconv.Atoi(os.Getenv("GITHUB_POLL_SECONDS"))
	if pollSec <= 0 {
		pollSec = 60
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	// Readloop for requests on stdin; poll in background after initialize.
	seen := map[int64]struct{}{}
	pollStop := make(chan struct{})

	go func() {
		// Wait until initialize has been answered (simple delay).
		time.Sleep(500 * time.Millisecond)
		ticker := time.NewTicker(time.Duration(pollSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollStop:
				return
			case <-ticker.C:
				if token == "" || owner == "" || repo == "" || project == "" {
					continue
				}
				issues, err := listIssues(token, owner, repo, label)
				if err != nil {
					fmt.Fprintf(os.Stderr, "github poll: %v\n", err)
					continue
				}
				for _, iss := range issues {
					if _, ok := seen[iss.Number]; ok {
						continue
					}
					seen[iss.Number] = struct{}{}
					_ = enc.Encode(map[string]any{
						"jsonrpc": "2.0",
						"method":  "trigger.issue",
						"params": map[string]any{
							"project":     project,
							"title":       iss.Title,
							"body":        iss.Body,
							"source":      "github",
							"external_id": fmt.Sprintf("%s/%s#%d", owner, repo, iss.Number),
						},
					})
				}
			}
		}
	}()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(response{JSONRPC: "2.0", Result: map[string]any{"ok": true, "port": "trigger"}, ID: req.ID})
		case "trigger.health":
			_ = enc.Encode(response{JSONRPC: "2.0", Result: map[string]any{"ok": true}, ID: req.ID})
		default:
			if req.ID != nil {
				_ = enc.Encode(response{JSONRPC: "2.0", Error: &rpcError{Code: -32601, Message: "method not found"}, ID: req.ID})
			}
		}
	}
	close(pollStop)
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
}

type ghIssue struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

func listIssues(token, owner, repo, label string) ([]ghIssue, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues?state=open&labels=%s&per_page=20", owner, repo, label)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var issues []ghIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, err
	}
	return issues, nil
}
