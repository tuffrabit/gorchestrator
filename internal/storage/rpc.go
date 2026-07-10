package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
)

// RPC is a StoragePort that forwards operations to a JSON-RPC stdio adapter
// (e.g. S3). Keys remain slash-canonical relative paths.
type RPC struct {
	client callClient
	name   string
}

type callClient interface {
	Call(ctx context.Context, method string, params any) (*adapters.Response, error)
	Close() error
}

// NewRPCPort wraps a supervised or plain JSON-RPC client as StoragePort.
func NewRPCPort(client callClient, name string) *RPC {
	return &RPC{client: client, name: name}
}

// Read implements Port.
func (r *RPC) Read(ctx context.Context, path string) ([]byte, error) {
	resp, err := r.client.Call(ctx, "storage.read", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	var out struct {
		Content []byte `json:"content"`
		// content may also be a base64 string or raw string depending on adapter;
		// prefer "content" as string for JSON friendliness.
		ContentStr string `json:"content_str"`
		Exists     bool   `json:"exists"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	if !out.Exists && len(out.Content) == 0 && out.ContentStr == "" {
		return nil, fmt.Errorf("not found: %s", path)
	}
	if len(out.Content) > 0 {
		return out.Content, nil
	}
	return []byte(out.ContentStr), nil
}

// Write implements Port.
func (r *RPC) Write(ctx context.Context, path string, data []byte) error {
	_, err := r.client.Call(ctx, "storage.write", map[string]any{
		"path":    path,
		"content": string(data),
	})
	return err
}

// List implements Port.
func (r *RPC) List(ctx context.Context, path string) ([]DirEntry, error) {
	resp, err := r.client.Call(ctx, "storage.list", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	var out struct {
		Entries []DirEntry `json:"entries"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// Exists implements Port.
func (r *RPC) Exists(ctx context.Context, path string) (bool, error) {
	resp, err := r.client.Call(ctx, "storage.exists", map[string]any{"path": path})
	if err != nil {
		return false, err
	}
	var out struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return false, err
	}
	return out.Exists, nil
}

// Mkdir implements Port.
func (r *RPC) Mkdir(ctx context.Context, path string) error {
	_, err := r.client.Call(ctx, "storage.mkdir", map[string]any{"path": path})
	return err
}

// Close stops the underlying client if supported.
func (r *RPC) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}
