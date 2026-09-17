package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Controller drives model residency on a swapping inference server: ensure
// the phase's model is loaded before work, unload after the phase's artifact
// is persisted. Both operations are idempotent — crash recovery may leave a
// model loaded.
type Controller interface {
	// EnsureLoaded loads model (or confirms it resident). timeout covers the
	// whole warmup request: llama-swap holds it while loading, and big-model
	// loads take minutes, so callers pass the phase flavor's model timeout.
	EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error
	// UnloadAll unloads every resident model and waits until nothing is
	// running. An error means residency is unknown — callers must not start
	// the next phase against the server.
	UnloadAll(ctx context.Context) error
}

// NewController builds the lifecycle controller for the configured inference
// server, or nil when no inference block is configured (implicit swapping).
func NewController(inf config.InferenceConfig) Controller {
	if inf.Type == "" {
		return nil
	}
	return newLlamaSwapController(inf.BaseURL)
}

// llamaSwapController is the llama-swap implementation of Controller.
type llamaSwapController struct {
	base string
	// pollInterval and unloadDeadline bound the post-unload /running poll.
	pollInterval   time.Duration
	unloadDeadline time.Duration
}

func newLlamaSwapController(baseURL string) *llamaSwapController {
	return &llamaSwapController{
		base:           strings.TrimRight(baseURL, "/"),
		pollInterval:   2 * time.Second,
		unloadDeadline: 2 * time.Minute,
	}
}

// EnsureLoaded implements Controller. The warmup is deliberately minimal
// (max_tokens 1): llama-swap holds the request while loading, so a completed
// warmup means the model is resident, and the short prompt keeps prompt-
// processing cost to nothing even on the slow model.
func (c *llamaSwapController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	warmup := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	}
	data, err := json.Marshal(warmup)
	if err != nil {
		return fmt.Errorf("marshal warmup request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create warmup request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("warmup %s: %w", model, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read warmup response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("warmup %s: status %d: %s", model, resp.StatusCode, cappedText(string(body)))
	}

	running, err := c.runningModels(ctx)
	if err != nil {
		return fmt.Errorf("verify %s loaded: %w", model, err)
	}
	for _, r := range running {
		if r == model {
			return nil
		}
	}
	return fmt.Errorf("warmup completed but %s is not running (loaded: %s)", model, strings.Join(running, ", "))
}

// UnloadAll implements Controller. Already-unloaded is success.
func (c *llamaSwapController) UnloadAll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/unload", nil)
	if err != nil {
		return fmt.Errorf("create unload request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unload: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read unload response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unload: status %d: %s", resp.StatusCode, cappedText(string(body)))
	}

	deadline := time.Now().Add(c.unloadDeadline)
	for {
		running, err := c.runningModels(ctx)
		if err != nil {
			return fmt.Errorf("poll running after unload: %w", err)
		}
		if len(running) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("models still running %s after unload: %s", c.unloadDeadline, strings.Join(running, ", "))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll running after unload: %w", ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}

// runningModels returns the names of models llama-swap currently has loaded.
func (c *llamaSwapController) runningModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/running", nil)
	if err != nil {
		return nil, fmt.Errorf("create running request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read running response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, cappedText(string(body)))
	}
	var parsed struct {
		Running []struct {
			Model string `json:"model"`
		} `json:"running"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse running response: %w", err)
	}
	out := make([]string, 0, len(parsed.Running))
	for _, r := range parsed.Running {
		out = append(out, r.Model)
	}
	return out, nil
}
