// Jira trigger adapter (JSON-RPC over stdio).
// Polls JQL for issues and emits trigger.issue notifications.
// Auth/env: JIRA_BASE_URL, JIRA_EMAIL, JIRA_API_TOKEN, JIRA_JQL, JIRA_PROJECT (gorchestrator project),
// JIRA_POLL_SECONDS (default 60).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	base := os.Getenv("JIRA_BASE_URL")
	email := os.Getenv("JIRA_EMAIL")
	token := os.Getenv("JIRA_API_TOKEN")
	jql := os.Getenv("JIRA_JQL")
	if jql == "" {
		jql = "statusCategory != Done ORDER BY updated DESC"
	}
	project := os.Getenv("JIRA_PROJECT")
	pollSec, _ := strconv.Atoi(os.Getenv("JIRA_POLL_SECONDS"))
	if pollSec <= 0 {
		pollSec = 60
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	seen := map[string]struct{}{}
	pollStop := make(chan struct{})

	go func() {
		time.Sleep(500 * time.Millisecond)
		ticker := time.NewTicker(time.Duration(pollSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollStop:
				return
			case <-ticker.C:
				if base == "" || email == "" || token == "" || project == "" {
					continue
				}
				issues, err := searchJira(base, email, token, jql)
				if err != nil {
					fmt.Fprintf(os.Stderr, "jira poll: %v\n", err)
					continue
				}
				for _, iss := range issues {
					if _, ok := seen[iss.Key]; ok {
						continue
					}
					seen[iss.Key] = struct{}{}
					_ = enc.Encode(map[string]any{
						"jsonrpc": "2.0",
						"method":  "trigger.issue",
						"params": map[string]any{
							"project":     project,
							"title":       iss.Fields.Summary,
							"body":        iss.Fields.Description,
							"source":      "jira",
							"external_id": iss.Key,
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

type jiraSearch struct {
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
		} `json:"fields"`
	} `json:"issues"`
}

func searchJira(base, email, token, jql string) ([]struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
	} `json:"fields"`
}, error) {
	u := stringsTrimSlash(base) + "/rest/api/3/search?jql=" + url.QueryEscape(jql) + "&maxResults=20&fields=summary,description"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var out jiraSearch
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Issues, nil
}

func stringsTrimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
