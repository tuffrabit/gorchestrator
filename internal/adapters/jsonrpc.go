package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxScanTokenSize = 10 * 1024 * 1024 // 10MB
	closeTimeout     = 5 * time.Second
	defaultNotifBuf  = 64
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

// Notification is a JSON-RPC 2.0 notification (no id) received from the adapter.
type Notification struct {
	Method string
	Params json.RawMessage
}

// ClientOptions configures how an adapter process is spawned.
type ClientOptions struct {
	// Env is the full environment for the child process. If nil, os.Environ() is used.
	// Adapters own their credentials via env (see spec §17 Q12).
	Env []string
	// ExtraEnv is appended to os.Environ() when Env is nil.
	ExtraEnv []string
	// NotifBuffer is the capacity of the notifications channel (default 64).
	NotifBuffer int
}

// Client manages a JSON-RPC stdio adapter process.
type Client struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   io.Reader
	nextID   int64
	pending  map[int64]chan *Response
	mu       sync.Mutex
	done     chan struct{}
	scanErr  atomic.Value
	notifCh  chan Notification
	closed   atomic.Bool
}

// NewClient spawns an external adapter binary and performs the initialize handshake.
func NewClient(binary string) (*Client, error) {
	return NewClientWithOptions(binary, ClientOptions{})
}

// NewClientWithOptions spawns an adapter with the given options.
func NewClientWithOptions(binary string, opts ClientOptions) (*Client, error) {
	cmd := exec.Command(binary)
	if opts.Env != nil {
		cmd.Env = opts.Env
	} else if len(opts.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), opts.ExtraEnv...)
	}
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

	buf := opts.NotifBuffer
	if buf <= 0 {
		buf = defaultNotifBuf
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
		notifCh: make(chan Notification, buf),
	}

	go c.logStderr()
	go c.readLoop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "initialize", map[string]any{}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize handshake: %w", err)
	}

	return c, nil
}

// Notifications returns a channel of JSON-RPC notifications from the adapter.
// The channel is closed when the client process exits or Close is called.
func (c *Client) Notifications() <-chan Notification {
	return c.notifCh
}

// Done is closed when the adapter process has exited (or failed to read).
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Pid returns the child process id, or 0 if unknown.
func (c *Client) Pid() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      *int64          `json:"id,omitempty"`
}

func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.notifCh)

	for c.stdout.Scan() {
		line := c.stdout.Bytes()
		var msg wireMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("jsonrpc: dropping malformed message: %v", err)
			continue
		}

		// Notification: has method, no id.
		if msg.Method != "" && msg.ID == nil {
			n := Notification{Method: msg.Method, Params: msg.Params}
			select {
			case c.notifCh <- n:
			default:
				log.Printf("jsonrpc: dropping notification %s (buffer full)", msg.Method)
			}
			continue
		}

		// Response: has id.
		if msg.ID == nil {
			log.Printf("jsonrpc: dropping message with neither method nor id")
			continue
		}
		resp := &Response{
			JSONRPC: msg.JSONRPC,
			Result:  msg.Result,
			Error:   msg.Error,
			ID:      *msg.ID,
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	if err := c.stdout.Err(); err != nil {
		c.scanErr.Store(err)
	}
}

func (c *Client) logStderr() {
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		pid := 0
		if c.cmd.Process != nil {
			pid = c.cmd.Process.Pid
		}
		log.Printf("adapter[%d] stderr: %s", pid, scanner.Text())
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
	if c.closed.Load() {
		return nil, fmt.Errorf("adapter client closed")
	}
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
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
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
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("adapter process exited")
	}
}

// Close terminates the adapter process.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		// Already closed; wait for done.
		<-c.done
		return nil
	}
	_ = c.stdin.Close()

	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		<-c.done
		return nil
	case <-time.After(closeTimeout):
		if err := c.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill adapter: %w", err)
		}
		<-done
		<-c.done
		return nil
	}
}
