package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vaayne/anna/internal/sandbox"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestNewHTTPClientAppliesConfiguredHeaders(t *testing.T) {
	client := newHTTPClient(15, map[string]string{
		"Authorization": "Bearer token",
		"X-Test":        "value",
	})
	if client.Timeout.Seconds() != 15 {
		t.Fatalf("timeout = %v, want 15s", client.Timeout)
	}

	rt, ok := client.Transport.(headerRoundTripper)
	if !ok {
		t.Fatalf("client.Transport = %T, want headerRoundTripper", client.Transport)
	}
	rt.base = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer token")
		}
		if got := req.Header.Get("X-Test"); got != "value" {
			t.Fatalf("X-Test = %q, want %q", got, "value")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})

	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = res.Body.Close()
}

func TestNewTransportStdioRequiresHost(t *testing.T) {
	_, err := newTransport(ServerConfig{Name: "demo", Transport: TransportStdio, Command: "demo"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var errSandboxHostRequired ErrSandboxHostRequired
	if !errors.As(err, &errSandboxHostRequired) {
		t.Fatalf("error = %T, want ErrSandboxHostRequired", err)
	}
}

func TestNewTransportStdioUsesHostStartProcess(t *testing.T) {
	host := &stdioHostStub{}
	transport, err := newTransport(ServerConfig{Name: "demo", Transport: TransportStdio, Command: "demo", Args: []string{"--flag"}, Env: map[string]string{"A": "1"}, TimeoutSeconds: 9}, host)
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	if _, ok := transport.(*officialmcp.IOTransport); !ok {
		t.Fatalf("transport = %T, want *mcp.IOTransport", transport)
	}
	if host.req.Path != "demo" {
		t.Fatalf("path = %q, want demo", host.req.Path)
	}
	if len(host.req.Args) != 1 || host.req.Args[0] != "--flag" {
		t.Fatalf("args = %#v", host.req.Args)
	}
	if host.req.Env["A"] != "1" {
		t.Fatalf("env = %#v", host.req.Env)
	}
	if host.req.Timeout.Seconds() != 9 {
		t.Fatalf("timeout = %v", host.req.Timeout)
	}
}

func TestNewTransportRemoteLogsExplicitException(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	previous := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(previous)

	transport, err := newTransport(ServerConfig{Name: "demo", Transport: TransportSSE, URL: "https://example.com/sse", TimeoutSeconds: 5}, nil)
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	if _, ok := transport.(*officialmcp.SSEClientTransport); !ok {
		t.Fatalf("transport = %T, want *mcp.SSEClientTransport", transport)
	}
	text := logs.String()
	if !strings.Contains(text, "sandbox.exception_path") {
		t.Fatalf("logs missing exception event: %q", text)
	}
	if !strings.Contains(text, "exception_id=EX-009") {
		t.Fatalf("logs missing EX-009 marker: %q", text)
	}
	if !strings.Contains(text, "transport dials outside sandbox.Host mediation") {
		t.Fatalf("logs missing trust-boundary detail: %q", text)
	}
}

type stdioHostStub struct {
	req sandbox.ProcessRequest
}

func (h *stdioHostStub) ReadFile(context.Context, string, int, int) (sandbox.ReadResult, error) {
	return sandbox.ReadResult{}, nil
}

func (h *stdioHostStub) WriteFile(context.Context, string, []byte) (sandbox.WriteResult, error) {
	return sandbox.WriteResult{}, nil
}

func (h *stdioHostStub) EditFile(context.Context, string, []sandbox.Edit) (sandbox.EditResult, error) {
	return sandbox.EditResult{}, nil
}

func (h *stdioHostStub) Stat(context.Context, string) (sandbox.StatResult, error) {
	return sandbox.StatResult{}, nil
}
func (h *stdioHostStub) ListDir(context.Context, string) ([]sandbox.DirEntry, error) { return nil, nil }
func (h *stdioHostStub) MkdirAll(context.Context, string, uint32) error              { return nil }
func (h *stdioHostStub) Remove(context.Context, string, bool) error                  { return nil }
func (h *stdioHostStub) Rename(context.Context, string, string) error                { return nil }
func (h *stdioHostStub) CreateTemp(context.Context, string, string) (sandbox.TempFile, error) {
	return nil, nil
}

func (h *stdioHostStub) Exec(context.Context, string, sandbox.ExecOptions) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{}, nil
}

func (h *stdioHostStub) StartProcess(_ context.Context, req sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	h.req = req
	return stdioProcessStub{}, nil
}

func (h *stdioHostStub) HTTPRequest(context.Context, sandbox.HTTPOptions) (sandbox.HTTPResult, error) {
	return sandbox.HTTPResult{}, nil
}

func (h *stdioHostStub) OpenHTTPStream(context.Context, sandbox.HTTPOptions) (sandbox.HTTPStream, error) {
	return nil, nil
}
func (h *stdioHostStub) ResolvePath(path string) (string, error) { return path, nil }
func (h *stdioHostStub) WorkingDir() string                      { return "/" }

type stdioProcessStub struct{}

func (stdioProcessStub) PID() int { return 1 }
func (stdioProcessStub) Wait(context.Context) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{}, nil
}
func (stdioProcessStub) Stdin() io.WriteCloser { return nopWriteCloser{&bytes.Buffer{}} }
func (stdioProcessStub) Stdout() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (stdioProcessStub) Stderr() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (stdioProcessStub) Close() error          { return nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestFlattenEnvIsDeterministic(t *testing.T) {
	got := flattenEnv(map[string]string{"B": "2", "A": "1"})
	want := []string{"A=1", "B=2"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
