// Package boxshclient provides a JSON-RPC client for the boxsh sandbox process.
package boxshclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Client manages a boxsh --rpc subprocess and provides typed RPC methods.
type Client struct {
	binaryPath string

	mu          sync.Mutex
	writeMu     sync.Mutex
	stderrMu    sync.RWMutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	stderr      io.ReadCloser
	closed      bool
	closing     bool
	started     bool
	idCounter   uint64
	pending     map[uint64]chan responseWrapper
	processDone chan struct{}
	waitErr     error
	readerErr   error
	stderrBuf   bytes.Buffer

	// sessionConfig holds the sandbox session configuration.
	sessionConfig SessionConfig
}

// SessionConfig configures a boxsh sandbox session.
type SessionConfig struct {
	// Src is the source workspace (read-only lower layer).
	Src string
	// Dst is the destination overlay root exposed inside the sandbox.
	Dst string
	// Cwd is the working directory inside the sandbox.
	Cwd string
	// ReadOnlyDirs are additional host directories bound read-only into the
	// sandbox so executables from PATH remain usable.
	ReadOnlyDirs []string
	// Network mode: disabled, allow_all, or whitelist.
	NetworkMode string
	// NetworkAllowlist is kept for config compatibility. Current boxsh only
	// supports allow-all or fully disabled networking.
	NetworkAllowlist []string
}

// Request is a JSON-RPC request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      uint64          `json:"id,omitempty"`
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
		pending:       make(map[uint64]chan responseWrapper),
	}
}

// Start launches the boxsh --rpc subprocess and initializes the session.
func (c *Client) Start(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "sandbox.boxsh.client_start",
		trace.WithAttributes(commonTraceAttrs(c.sessionConfig)...),
	)
	defer span.End()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		err := fmt.Errorf("boxshclient: client is closed")
		recordTraceError(span, err)
		return err
	}
	if c.started {
		c.mu.Unlock()
		err := fmt.Errorf("boxshclient: client already started")
		recordTraceError(span, err)
		return err
	}
	c.mu.Unlock()

	args, err := c.buildArgs()
	if err != nil {
		recordTraceError(span, err)
		return err
	}
	span.SetAttributes(attribute.Int("anna.sandbox.arg_count", len(args)))
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		err = fmt.Errorf("boxshclient: stdin pipe: %w", err)
		recordTraceError(span, err)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("boxshclient: stdout pipe: %w", err)
		recordTraceError(span, err)
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("boxshclient: stderr pipe: %w", err)
		recordTraceError(span, err)
		return err
	}

	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("boxshclient: start: %w", err)
		recordTraceError(span, err)
		return err
	}
	span.SetAttributes(attribute.Int("process.pid", cmd.Process.Pid))

	processDone := make(chan struct{})

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.stderr = stderr
	c.started = true
	c.closing = false
	c.waitErr = nil
	c.readerErr = nil
	c.processDone = processDone
	c.pending = make(map[uint64]chan responseWrapper)
	c.stderrMu.Lock()
	c.stderrBuf.Reset()
	c.stderrMu.Unlock()
	c.mu.Unlock()

	go c.readLoop(stdout)
	go c.waitLoop(cmd, processDone)
	go c.captureStderr(stderr)

	if err := c.handshake(ctx); err != nil {
		recordTraceError(span, err)
		diagnostics := c.snapshotDiagnostics()
		slog.Warn("boxsh client handshake failed",
			"component", "boxsh_client",
			"binary", c.binaryPath,
			"args", args,
			"src", c.sessionConfig.Src,
			"dst", c.sessionConfig.Dst,
			"cwd", c.sessionConfig.Cwd,
			"readonly_dirs", uniqueCleanAbsPaths(c.sessionConfig.ReadOnlyDirs),
			"network_mode", c.sessionConfig.NetworkMode,
			"wait_err", diagnostics.waitErr,
			"reader_err", diagnostics.readerErr,
			"stderr", diagnostics.stderr,
			"error", err,
		)
		_ = c.Close()
		return fmt.Errorf("boxshclient: handshake: %w", err)
	}

	span.AddEvent("sandbox.boxsh.client.ready")
	return nil
}

// buildArgs constructs the boxsh command-line arguments.
func (c *Client) buildArgs() ([]string, error) {
	args := []string{"--rpc", "--sandbox"}

	if c.sessionConfig.Src == "" || c.sessionConfig.Dst == "" {
		return nil, fmt.Errorf("boxshclient: src and dst are required")
	}
	args = append(args, "--bind", fmt.Sprintf("cow:%s:%s", c.sessionConfig.Src, c.sessionConfig.Dst))
	for _, dir := range uniqueCleanAbsPaths(c.sessionConfig.ReadOnlyDirs) {
		args = append(args, "--bind", fmt.Sprintf("ro:%s", dir))
	}

	switch c.sessionConfig.NetworkMode {
	case "", NetworkDisabled:
		args = append(args, "--new-net-ns")
	case NetworkAllowAll:
		// no-op
	case NetworkWhitelist:
		return nil, fmt.Errorf("boxshclient: whitelist network mode is not supported by boxsh 2.0.1")
	default:
		return nil, fmt.Errorf("boxshclient: unsupported network mode %q", c.sessionConfig.NetworkMode)
	}

	return args, nil
}

// handshake verifies the session is ready using boxsh's initialize request.
func (c *Client) handshake(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "sandbox.boxsh.handshake",
		trace.WithAttributes(commonTraceAttrs(c.sessionConfig)...),
	)
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "anna",
			"version": "dev",
		},
	}, &result); err != nil {
		recordTraceError(span, err)
		return err
	}
	if !strings.EqualFold(result.ServerInfo.Name, "boxsh") {
		err := fmt.Errorf("unexpected initialize response from %q", result.ServerInfo.Name)
		recordTraceError(span, err)
		return err
	}
	span.SetAttributes(
		attribute.String("anna.sandbox.server.name", result.ServerInfo.Name),
		attribute.String("anna.sandbox.server.version", result.ServerInfo.Version),
		attribute.String("anna.sandbox.protocol_version", result.ProtocolVersion),
	)
	return nil
}

// Alive reports whether the boxsh process is still running.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started || c.closed {
		return false
	}
	if c.readerErr != nil || c.waitErr != nil {
		return false
	}
	return !done(c.processDone)
}

// Close shuts down the boxsh process and cleans up resources.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	if !c.started {
		c.closed = true
		c.mu.Unlock()
		return nil
	}
	if c.closing {
		processDone := c.processDone
		c.mu.Unlock()
		select {
		case <-processDone:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("boxshclient: close timeout waiting for process exit")
		}
	}

	c.closing = true
	stdin := c.stdin
	cmd := c.cmd
	processDone := c.processDone
	c.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}

	if processDone != nil {
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
				<-processDone
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.readerErr
	if err == nil && c.waitErr != nil && !errors.Is(c.waitErr, io.EOF) {
		err = c.waitErr
	}
	c.closed = true
	c.closing = false
	c.stdin = nil
	c.stdout = nil
	c.stderr = nil
	c.cmd = nil
	c.pending = nil
	return err
}

func (c *Client) waitLoop(cmd *exec.Cmd, processDone chan struct{}) {
	err := cmd.Wait()

	c.mu.Lock()
	if c.waitErr == nil {
		if err != nil {
			c.waitErr = fmt.Errorf("boxshclient: process exit: %w", err)
		} else {
			c.waitErr = io.EOF
		}
		if c.closed || c.closing {
			c.waitErr = nil
		}
	}
	pending := c.drainPendingLocked(c.terminalErrLocked())
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- responseWrapper{err: c.terminalErr()}
	}
	close(processDone)
}

func (c *Client) captureStderr(stderr io.Reader) {
	if stderr == nil {
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			c.stderrMu.Lock()
			_, _ = c.stderrBuf.Write(buf[:n])
			c.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// readLoop continuously reads JSON-RPC responses from stdout.
func (c *Client) readLoop(stdout io.Reader) {
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	for {
		var resp Response
		if err := decoder.Decode(&resp); err != nil {
			if err != io.EOF {
				c.mu.Lock()
				if c.closing || c.closed || isExpectedReaderClose(err) {
					c.mu.Unlock()
					return
				}
				if c.readerErr == nil {
					c.readerErr = fmt.Errorf("boxshclient: decode error: %w", err)
				}
				pending := c.drainPendingLocked(c.terminalErrLocked())
				c.mu.Unlock()
				for _, ch := range pending {
					ch <- responseWrapper{err: c.terminalErr()}
				}
			}
			return
		}

		c.mu.Lock()
		ch := c.pending[resp.ID]
		c.mu.Unlock()
		if ch != nil {
			ch <- responseWrapper{resp: &resp}
		}
	}
}

// call performs a synchronous JSON-RPC call.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	id := atomic.AddUint64(&c.idCounter, 1)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("boxshclient: marshal params: %w", err)
	}

	req := Request{JSONRPC: "2.0", Method: method, Params: paramsJSON, ID: id}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("boxshclient: marshal request: %w", err)
	}

	respCh, stdin, processDone, err := c.registerCall(id, method)
	if err != nil {
		return err
	}
	defer c.unregisterCall(id)

	c.writeMu.Lock()
	_, err = fmt.Fprintf(stdin, "%s\n", reqJSON)
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("boxshclient: write request: %w", err)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("boxshclient: call %s: %w", method, ctx.Err())
	case <-processDone:
		return c.terminalErr()
	case wrap := <-respCh:
		if wrap.err != nil {
			return wrap.err
		}
		if wrap.resp == nil {
			return fmt.Errorf("boxshclient: empty response for %s", method)
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

func (c *Client) registerCall(id uint64, method string) (chan responseWrapper, io.WriteCloser, <-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, nil, nil, fmt.Errorf("boxshclient: client closed")
	}
	if c.closing {
		return nil, nil, nil, fmt.Errorf("boxshclient: client is shutting down")
	}
	if !c.started || c.stdin == nil {
		return nil, nil, nil, fmt.Errorf("boxshclient: client not started")
	}
	if c.readerErr != nil || c.waitErr != nil || done(c.processDone) {
		return nil, nil, nil, c.terminalErrLocked()
	}

	respCh := make(chan responseWrapper, 1)
	c.pending[id] = respCh
	return respCh, c.stdin, c.processDone, nil
}

func (c *Client) unregisterCall(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		delete(c.pending, id)
	}
}

func (c *Client) drainPendingLocked(err error) []chan responseWrapper {
	if len(c.pending) == 0 {
		return nil
	}
	channels := make([]chan responseWrapper, 0, len(c.pending))
	for id, ch := range c.pending {
		delete(c.pending, id)
		if ch != nil {
			channels = append(channels, ch)
		}
	}
	return channels
}

func (c *Client) terminalErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalErrLocked()
}

func (c *Client) terminalErrLocked() error {
	if c.readerErr != nil {
		return c.readerErr
	}
	if c.waitErr != nil && !errors.Is(c.waitErr, io.EOF) {
		return c.waitErr
	}
	if c.closed {
		return fmt.Errorf("boxshclient: client closed")
	}
	if c.closing {
		return fmt.Errorf("boxshclient: client is shutting down")
	}
	if !c.started {
		return fmt.Errorf("boxshclient: client not started")
	}
	return fmt.Errorf("boxshclient: process exited")
}

type diagnosticsSnapshot struct {
	waitErr   error
	readerErr error
	stderr    string
}

func (c *Client) snapshotDiagnostics() diagnosticsSnapshot {
	c.mu.Lock()
	waitErr := c.waitErr
	readerErr := c.readerErr
	c.mu.Unlock()
	return diagnosticsSnapshot{
		waitErr:   waitErr,
		readerErr: readerErr,
		stderr:    c.Stderr(),
	}
}

// Stderr returns buffered stderr output from the boxsh process.
func (c *Client) Stderr() string {
	c.stderrMu.RLock()
	defer c.stderrMu.RUnlock()
	return strings.TrimSpace(c.stderrBuf.String())
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

// CreateSessionDir creates an ephemeral session directory for the overlay root.
// The caller is responsible for cleaning up the directory.
func CreateSessionDir(baseDir string) (string, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("boxshclient: create session base dir: %w", err)
	}

	sessionDir, err := os.MkdirTemp(baseDir, ".anna-boxsh-session-")
	if err != nil {
		return "", fmt.Errorf("boxshclient: create session dir: %w", err)
	}

	return sessionDir, nil
}

// CleanupSessionDir removes the ephemeral session directory and its metadata sidecar.
func CleanupSessionDir(sessionDir string) error {
	if sessionDir == "" {
		return nil
	}
	_ = os.Remove(sessionMetaPath(sessionDir))
	return os.RemoveAll(sessionDir)
}

// ResolveSandboxCwd determines the working directory inside the sandbox.
// If workDir is empty or not under the user root, it defaults to the user root.
func ResolveSandboxCwd(userRoot, workDir string) string {
	if workDir == "" {
		return userRoot
	}

	candidate := workDir
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(userRoot, candidate)
	}
	candidate = filepath.Clean(candidate)

	if !isWithinRoot(userRoot, candidate) {
		return userRoot
	}

	return candidate
}

func isWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == "" || !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return false
	}
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func done(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func isExpectedReaderClose(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "file already closed") || strings.Contains(msg, "closed pipe")
}

func uniqueCleanAbsPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
