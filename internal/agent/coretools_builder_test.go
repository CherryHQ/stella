package agent

import (
	"context"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type fakeSession struct {
	alive  bool
	policy pkgsandbox.Policy
}

func (f *fakeSession) Policy() pkgsandbox.Policy { return f.policy }
func (f *fakeSession) Close() error              { return nil }
func (f *fakeSession) Alive() bool               { return f.alive }
func (f *fakeSession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (f *fakeSession) Exec(_ context.Context, _ string, _ pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (f *fakeSession) StartProcess(_ context.Context, _ pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	return nil, nil
}
func (f *fakeSession) ResolvePath(path string) (string, error)      { return path, nil }
func (f *fakeSession) ResolveWritePath(path string) (string, error) { return path, nil }
func (f *fakeSession) WorkingDir() string                           { return "/tmp" }

func TestBuildSandboxCoreTools_NoSessionFailsClosed(t *testing.T) {
	tools := buildSandboxCoreTools(nil, pkgplugins.ToolBuildContext{Paths: pkgplugins.ToolPaths{ToolsBinDir: "/tmp/bin"}}, nil, nil)
	if tools != nil {
		t.Fatalf("expected no tools without sandbox session, got %v", tools)
	}
}

func TestBuildSandboxCoreTools_WithSessionUsesHostTools(t *testing.T) {
	session := &fakeSession{alive: true}
	tools := buildSandboxCoreTools(session, pkgplugins.ToolBuildContext{}, nil, nil)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	gotNames := []string{tools[0].Definition().Name, tools[1].Definition().Name, tools[2].Definition().Name, tools[3].Definition().Name}
	want := []string{"bash", "read", "write", "edit"}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, gotNames[i], want[i])
		}
	}
}
