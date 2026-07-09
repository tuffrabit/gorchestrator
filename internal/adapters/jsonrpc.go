package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxScanTokenSize = 10 * 1024 * 1024 // 10MB
	closeTimeout     = 5 * time.Second
)

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      int64           `json:"id"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Client manages a JSON-RPC stdio adapter process.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	stderr  io.Reader
	nextID  int64
	pending map[int64]chan *Response
	mu      sync.Mutex
	done    chan struct{}
	scanErr atomic.Value
}

// NewClient spawns an external adapter binary and performs the initialize handshake.
func NewClient(binary string) (*Client, error) {
	cmd := exec.Command(binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start adapter: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxScanTokenSize)

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  scanner,
		stderr:  stderr,
		pending: make(map[int64]chan *Response),
		done:    make(chan struct{}),
	}

	// Capture stderr in the background.
	go c.logStderr()
	go c.readLoop()

	// Perform initialize handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "initialize", map[string]any{}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize handshake: %w", err)
	}

	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for c.stdout.Scan() {
		line := c.stdout.Bytes()
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			log.Printf("jsonrpc: dropping malformed response: %v", err)
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
	if err := c.stdout.Err(); err != nil {
		c.scanErr.Store(err)
	}
}

func (c *Client) logStderr() {
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		log.Printf("adapter[%d] stderr: %s", c.cmd.Process.Pid, scanner.Text())
	}
}

// ScanError returns the scanner error, if any, after the read loop exits.
func (c *Client) ScanError() error {
	if v := c.scanErr.Load(); v != nil {
		return v.(error)
	}
	return nil
}

// Call sends a JSON-RPC request and waits for a response.
func (c *Client) Call(ctx context.Context, method string, params any) (*Response, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	id := atomic.AddInt64(&c.nextID, 1)
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  data,
		ID:      id,
	}
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	reqData = append(reqData, '\n')
	if _, err := c.stdin.Write(reqData); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("adapter process exited")
	}
}

// Close terminates the adapter process.
func (c *Client) Close() error {
	_ = c.stdin.Close()

	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(closeTimeout):
		if err := c.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill adapter: %w", err)
		}
		<-done
		return nil
	}
}
