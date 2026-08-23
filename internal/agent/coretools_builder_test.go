package agent

import (
	"context"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type fakeSession struct {
	alive    bool
	policy   pkgsandbox.Policy
	lastExec pkgsandbox.ExecOptions
}

func (f *fakeSession) Policy() pkgsandbox.Policy { return f.policy }
func (f *fakeSession) Close() error              { return nil }
func (f *fakeSession) Alive() bool               { return f.alive }
func (f *fakeSession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (f *fakeSession) Exec(_ context.Context, _ string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	f.lastExec = opts
	return pkgsandbox.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (f *fakeSession) StartProcess(_ context.Context, _ pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	return nil, nil
}
func (f *fakeSession) Files() pkgsandbox.FileAccess { return pkgsandbox.NopSession().Files() }
func (f *fakeSession) WorkingDir() string           { return "/tmp" }

func TestBuildSandboxCoreTools_NoSessionFailsClosed(t *testing.T) {
	tools := buildSandboxCoreTools(nil, nil, nil)
	if tools != nil {
		t.Fatalf("expected no tools without sandbox session, got %v", tools)
	}
}

func TestBuildSandboxCoreTools_WithSessionUsesHostTools(t *testing.T) {
	session := &fakeSession{alive: true}
	tools := buildSandboxCoreTools(session, nil, nil)
	if len(tools) != 2 {
		t.Fatalf("expected bash and view_image without a vision model, got %d tools", len(tools))
	}
	for i, want := range []string{"bash", "view_image"} {
		if got := tools[i].Definition().Name; got != want {
			t.Fatalf("tool[%d] = %q, want %q", i, got, want)
		}
	}
	if _, err := tools[0].Execute(context.Background(), map[string]any{"command": "true"}); err != nil {
		t.Fatalf("execute bash: %v", err)
	}
	if session.lastExec.Cwd != session.WorkingDir() {
		t.Fatalf("bash cwd = %q, want canonical session cwd %q", session.lastExec.Cwd, session.WorkingDir())
	}
	if session.lastExec.Cwd == "/host/private/project" {
		t.Fatal("core tools used caller-provided physical project root")
	}
}
