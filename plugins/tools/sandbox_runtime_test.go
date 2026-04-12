package plugintools

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
)

func TestSandboxRuntimeFromBackendNil(t *testing.T) {
	got := SandboxRuntimeFromBackend(nil)
	if got == nil {
		t.Fatal("SandboxRuntimeFromBackend(nil) returned nil runtime")
	}
	if got.Enabled() {
		t.Fatal("expected disabled runtime when backend is nil")
	}
	if _, err := got.Exec(context.Background(), "echo hi", 1); err == nil {
		t.Fatal("expected unavailable sandbox error")
	}
}

func TestBoxshSandboxRuntimeDisabledWhenBackendNotAlive(t *testing.T) {
	runtime := SandboxRuntimeFromBackend(&boxshclient.SharedBackend{})
	if runtime == nil {
		t.Fatal("expected runtime")
	}
	if runtime.Enabled() {
		t.Fatal("expected runtime to be disabled")
	}
	if _, err := runtime.Exec(context.Background(), "echo hi", 1); err == nil {
		t.Fatal("expected backend-not-running error")
	}
}

func TestSandboxRuntimeFromHostExec(t *testing.T) {
	runtime := SandboxRuntimeFromHost(hostRuntimeStub{})
	if !runtime.Enabled() {
		t.Fatal("expected host runtime to be enabled")
	}
	got, err := runtime.Exec(context.Background(), "echo hi", 1)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got.Stdout != "ok" || got.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

type hostRuntimeStub struct{}

func (hostRuntimeStub) ReadFile(context.Context, string, int, int) (sandbox.ReadResult, error) {
	return sandbox.ReadResult{}, nil
}

func (hostRuntimeStub) WriteFile(context.Context, string, []byte) (sandbox.WriteResult, error) {
	return sandbox.WriteResult{}, nil
}

func (hostRuntimeStub) EditFile(context.Context, string, []sandbox.Edit) (sandbox.EditResult, error) {
	return sandbox.EditResult{}, nil
}

func (hostRuntimeStub) Stat(context.Context, string) (sandbox.StatResult, error) {
	return sandbox.StatResult{}, nil
}
func (hostRuntimeStub) ListDir(context.Context, string) ([]sandbox.DirEntry, error) { return nil, nil }
func (hostRuntimeStub) MkdirAll(context.Context, string, uint32) error              { return nil }
func (hostRuntimeStub) Remove(context.Context, string, bool) error                  { return nil }
func (hostRuntimeStub) Rename(context.Context, string, string) error                { return nil }
func (hostRuntimeStub) CreateTemp(context.Context, string, string) (sandbox.TempFile, error) {
	return nil, nil
}

func (hostRuntimeStub) Exec(context.Context, string, sandbox.ExecOptions) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (hostRuntimeStub) StartProcess(context.Context, sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	return nil, nil
}

func (hostRuntimeStub) HTTPRequest(context.Context, sandbox.HTTPOptions) (sandbox.HTTPResult, error) {
	return sandbox.HTTPResult{}, nil
}

func (hostRuntimeStub) OpenHTTPStream(context.Context, sandbox.HTTPOptions) (sandbox.HTTPStream, error) {
	return nil, nil
}
func (hostRuntimeStub) ResolvePath(path string) (string, error) { return path, nil }
func (hostRuntimeStub) WorkingDir() string                      { return "/tmp" }
