// Package boxshclient provides a JSON-RPC client for the boxsh sandbox process.
package boxshclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vaayne/anna/internal/config"
)

// Client manages a boxsh --rpc subprocess and provides typed RPC methods.
type Client struct {
	binaryPath string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderr     io.ReadCloser

	mu       sync.Mutex
	closed   bool
	idCounter uint64

	// respCh receives decoded responses keyed by request ID.
	respCh chan responseWrapper
	// errCh receives fatal errors from the reader goroutine.
	errCh chan error

	// sessionConfig holds the sandbox session configuration.
	sessionConfig SessionConfig
}

// SessionConfig configures a boxsh sandbox session.
type SessionConfig struct {
	// Src is the source workspace (read-only lower layer).
	Src string
	// Dst is the destination upperdir (read-write layer).
	Dst string
	// Cwd is the working directory inside the sandbox.
	Cwd string
	// Network mode: disabled, allow_all, or whitelist.
	NetworkMode string
	// NetworkAllowlist is the list of allowed hosts/CIDRs for whitelist mode.
	NetworkAllowlist []string
}

// Request is a JSON-RPC request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      uint64          `json:"id"`
}

// Response is a JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      uint64          `json:"id"`
}

// RPCError represents a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// responseWrapper carries a response or error for internal routing.
type responseWrapper struct {
	resp *Response
	err  error
}

// New creates a boxsh client with the given binary path and session configuration.
func New(binaryPath string, cfg SessionConfig) *Client {
	return &Client{
		binaryPath:    binaryPath,
		sessionConfig: cfg,
		respCh:        make(chan responseWrapper, 10),
		errCh:         make(chan error, 1),
	}
}

// Start launches the boxsh --rpc subprocess and initializes the session.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("boxshclient: client is closed")
	}

	args := c.buildArgs()
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("boxshclient: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("boxshclient: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("boxshclient: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("boxshclient: start: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReader(stdout)
	c.stderr = stderr

	// Start the response reader goroutine.
	go c.readLoop()

	// Perform health check / handshake.
	if err := c.handshake(ctx); err != nil {
		_ = c.closeInternal()
		return fmt.Errorf("boxshclient: handshake: %w", err)
	}

	return nil
}

// buildArgs constructs the boxsh command-line arguments.
func (c *Client) buildArgs() []string {
	args := []string{"--rpc"}

	if c.sessionConfig.Src != "" {
		args = append(args, "--src", c.sessionConfig.Src)
	}
	if c.sessionConfig.Dst != "" {
		args = append(args, "--dst", c.sessionConfig.Dst)
	}
	if c.sessionConfig.Cwd != "" {
		args = append(args, "--cwd", c.sessionConfig.Cwd)
	}

	// Network mode flags.
	switch c.sessionConfig.NetworkMode {
	case config.SandboxNetworkDisabled:
		args = append(args, "--net=none")
	case config.SandboxNetworkAllowAll:
		args = append(args, "--net=allow")
	case config.SandboxNetworkWhitelist:
		args = append(args, "--net=whitelist")
		for _, entry := range c.sessionConfig.NetworkAllowlist {
			args = append(args, "--allow", entry)
		}
	}

	return args
}

// handshake verifies the session is ready by calling a ping method.
func (c *Client) handshake(ctx context.Context) error {
	// Use a short timeout for handshake.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// The ping method simply echoes back. This validates the RPC channel.
	var result string
	if err := c.call(ctx, "ping", map[string]any{}, &result); err != nil {
		return err
	}
	if result != "pong" {
		return fmt.Errorf("unexpected ping response: %q", result)
	}
	return nil
}

// Alive reports whether the boxsh process is still running.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd == nil || c.closed {
		return false
	}

	// Non-blocking check if process has exited.
	select {
	case err := <-c.errCh:
		// Process has exited.
		_ = err
		return false
	default:
		return c.cmd.ProcessState == nil || !c.cmd.ProcessState.Exited()
	}
}

// Close shuts down the boxsh process and cleans up resources.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeInternal()
}

func (c *Client) closeInternal() error {
	if c.closed {
		return nil
	}
	c.closed = true

	// Signal the boxsh process to exit gracefully via RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.call(ctx, "quit", map[string]any{}, nil)

	// Close stdin to signal EOF.
	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	// Wait for process exit with timeout.
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- c.cmd.Wait()
		}()

		select {
		case <-done:
			// Process exited normally.
		case <-time.After(5 * time.Second):
			// Force kill after timeout.
			_ = c.cmd.Process.Kill()
			<-done
		}
	}

	close(c.respCh)
	return nil
}

// readLoop continuously reads JSON-RPC responses from stdout.
func (c *Client) readLoop() {
	decoder := json.NewDecoder(c.stdout)
	for {
		var resp Response
		if err := decoder.Decode(&resp); err != nil {
			if err != io.EOF {
				select {
				case c.errCh <- fmt.Errorf("boxshclient: decode error: %w", err):
				default:
				}
			}
			return
		}
		c.respCh <- responseWrapper{resp: &resp}
	}
}

// call performs a synchronous JSON-RPC call.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	id := atomic.AddUint64(&c.idCounter, 1)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("boxshclient: marshal params: %w", err)
	}

	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
		ID:      id,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("boxshclient: marshal request: %w", err)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("boxshclient: client closed")
	}
	stdin := c.stdin
	c.mu.Unlock()

	// Write the request followed by newline.
	if _, err := fmt.Fprintf(stdin, "%s\n", reqJSON); err != nil {
		return fmt.Errorf("boxshclient: write request: %w", err)
	}

	// Wait for response matching the request ID.
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("boxshclient: call %s: %w", method, ctx.Err())
		case err := <-c.errCh:
			return fmt.Errorf("boxshclient: reader error: %w", err)
		case wrap, ok := <-c.respCh:
			if !ok {
				return fmt.Errorf("boxshclient: response channel closed")
			}
			if wrap.err != nil {
				return wrap.err
			}
			if wrap.resp.ID != id {
				// Not our response, continue waiting.
				continue
			}
			if wrap.resp.Error != nil {
				return wrap.resp.Error
			}
			if result != nil && wrap.resp.Result != nil {
				if err := json.Unmarshal(wrap.resp.Result, result); err != nil {
					return fmt.Errorf("boxshclient: unmarshal result: %w", err)
				}
			}
			return nil
		}
	}
}

// Stderr returns the stderr output from the boxsh process (if captured).
func (c *Client) Stderr() string {
	// TODO: implement buffered stderr capture if needed.
	return ""
}

// PlatformSupportsBoxsh reports whether the current platform supports boxsh sandboxing.
func PlatformSupportsBoxsh() bool {
	switch runtime.GOOS {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

// CreateSessionDir creates an ephemeral session directory for the upperdir (DST).
// The caller is responsible for cleaning up the directory.
func CreateSessionDir(baseDir string) (string, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("boxshclient: create session base dir: %w", err)
	}

	sessionDir, err := os.MkdirTemp(baseDir, "boxsh-session-*")
	if err != nil {
		return "", fmt.Errorf("boxshclient: create session dir: %w", err)
	}

	return sessionDir, nil
}

// CleanupSessionDir removes the ephemeral session directory.
func CleanupSessionDir(sessionDir string) error {
	if sessionDir == "" {
		return nil
	}
	return os.RemoveAll(sessionDir)
}

// ResolveSandboxCwd determines the working directory inside the sandbox.
// If workDir is empty or not under the sandbox root, it defaults to the sandbox root.
func ResolveSandboxCwd(sandboxRoot, workDir string) string {
	if workDir == "" {
		return sandboxRoot
	}

	// Ensure workDir is absolute.
	if !filepath.IsAbs(workDir) {
		workDir = filepath.Join(sandboxRoot, workDir)
	}

	// Check if workDir is under sandboxRoot.
	rel, err := filepath.Rel(sandboxRoot, workDir)
	if err != nil || rel == "" || rel == "." || (len(rel) > 0 && rel[0] == '.') {
		// Not under sandbox root, default to sandbox root.
		return sandboxRoot
	}

	return workDir
}
