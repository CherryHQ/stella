package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/vaayne/anna/internal/pluginapi"
)

type Client struct {
	def    Definition
	logger *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	reqMu    sync.Mutex
	closeMu  sync.Mutex
	closed   atomic.Bool
	cancelMu sync.Mutex
	exitErr  atomic.Pointer[error]
	waitDone chan struct{}
}

type StartOptions struct {
	Logger *slog.Logger
}

const builtinRuntimeShutdownTimeout = 2 * time.Second

func Start(ctx context.Context, def Definition, opts StartOptions) (*Client, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	entrypoint := def.Entrypoint()
	if entrypoint == BuiltinEntrypoint {
		if override := os.Getenv("ANNA_PLUGIN_ENTRYPOINT"); override != "" {
			entrypoint = override
		} else {
			exePath, err := os.Executable()
			if err != nil {
				return nil, fmt.Errorf("resolve current executable: %w", err)
			}
			helperName := "anna-plugin" + filepath.Ext(exePath)
			entrypoint = filepath.Join(filepath.Dir(exePath), helperName)
		}
	}

	cmd := exec.CommandContext(ctx, entrypoint, def.Manifest.Args...)
	cmd.Dir = def.RootDir
	if def.Manifest.Metadata != nil && def.Manifest.Entrypoint == BuiltinEntrypoint {
		env := os.Environ()
		if v, ok := def.Manifest.Metadata["work_dir"].(string); ok && v != "" {
			env = append(env, "ANNA_PLUGIN_WORKDIR="+v)
		}
		if v, ok := def.Manifest.Metadata["user_data_dir"].(string); ok && v != "" {
			env = append(env, "ANNA_PLUGIN_USER_DATA_DIR="+v)
		}
		cmd.Env = env
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
		return nil, fmt.Errorf("start process: %w", err)
	}

	c := &Client{
		def:      def,
		logger:   logger.With("plugin", def.ID()),
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReader(stdout),
		waitDone: make(chan struct{}),
	}

	go c.consumeStderr(stderr)
	go c.waitProcess()

	var hs pluginapi.HandshakeResponse
	if err := c.Request(ctx, "handshake", pluginapi.HandshakeRequest{
		ProtocolVersion: pluginapi.ProtocolVersion,
	}, &hs); err != nil {
		_ = c.Close()
		return nil, err
	}

	if hs.ProtocolVersion != pluginapi.ProtocolVersion {
		_ = c.Close()
		return nil, fmt.Errorf("plugin %s protocol mismatch: got %q want %q", def.ID(), hs.ProtocolVersion, pluginapi.ProtocolVersion)
	}
	if hs.Name != def.Manifest.Name || hs.Kind != def.Manifest.Kind {
		_ = c.Close()
		return nil, fmt.Errorf("plugin %s handshake mismatch: got name=%q kind=%q", def.ID(), hs.Name, hs.Kind)
	}

	return c, nil
}

func (c *Client) Request(ctx context.Context, method string, params any, out any) error {
	if c.isClosed() {
		return errors.New("plugin client is closed")
	}
	return c.request(ctx, method, params, out)
}

func (c *Client) Health(ctx context.Context) error {
	var resp pluginapi.HealthResponse
	if err := c.Request(ctx, "health", struct{}{}, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New("plugin reported unhealthy")
	}
	return nil
}

func (c *Client) Alive() bool {
	select {
	case <-c.waitDone:
		return false
	default:
		return true
	}
}

func (c *Client) Wait() error {
	<-c.waitDone
	ptr := c.exitErr.Load()
	if ptr == nil {
		return nil
	}
	return *ptr
}

func (c *Client) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed.Load() {
		return nil
	}
	c.closed.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), builtinRuntimeShutdownTimeout)
	defer cancel()
	_ = c.request(ctx, "shutdown", struct{}{}, nil)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.Alive() {
		select {
		case <-c.waitDone:
		case <-time.After(builtinRuntimeShutdownTimeout):
			_ = c.cmd.Process.Kill()
		}
	}
	return c.Wait()
}

func (c *Client) waitProcess() {
	err := c.cmd.Wait()
	if err != nil {
		c.exitErr.Store(&err)
	}
	close(c.waitDone)
}

func (c *Client) consumeStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		c.logger.Info("plugin stderr", "line", line)
	}
	if err := scanner.Err(); err != nil {
		c.logger.Warn("plugin stderr read failed", "error", err)
	}
}

func (c *Client) isClosed() bool {
	return c.closed.Load()
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	payload, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	env := pluginapi.Envelope{
		ID:     uuid.NewString(),
		Type:   pluginapi.MessageTypeRequest,
		Method: method,
		Params: payload,
	}
	if err := writeEnvelope(c.stdin, env); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.abortCurrentRequest()
		case <-done:
		}
	}()
	defer close(done)

	resp, err := readEnvelope(c.stdout)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if resp.Type != pluginapi.MessageTypeResponse {
		return fmt.Errorf("unexpected message type %q", resp.Type)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) abortCurrentRequest() {
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if c.cmd != nil && c.Alive() && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
