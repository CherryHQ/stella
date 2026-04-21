package runner

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/sandbox"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func fakeRunnerSession(backend string, host sandbox.Host) *runnerSession {
	return &runnerSession{
		session: &fakeSession{host: host, alive: true},
		policy:  sandbox.Policy{Backend: backend},
	}
}

type fakeSession struct {
	host   sandbox.Host
	alive  bool
	policy sandbox.Policy
}

func (f *fakeSession) Host() sandbox.Host     { return f.host }
func (f *fakeSession) Policy() sandbox.Policy { return f.policy }
func (f *fakeSession) Close() error           { return nil }
func (f *fakeSession) Alive() bool            { return f.alive }
func (f *fakeSession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestBuildSandboxCoreTools_NoSessionFailsClosed(t *testing.T) {
	tools := buildSandboxCoreTools(nil, plugintools.BuildContext{Paths: pkgplugins.ToolPaths{ToolsBinDir: "/tmp/bin"}})
	if tools != nil {
		t.Fatalf("expected no tools without sandbox session, got %v", tools)
	}
}

func TestBuildSandboxCoreTools_WithSessionUsesHostTools(t *testing.T) {
	session := fakeRunnerSession("local", &fakeHost{})
	tools := buildSandboxCoreTools(session, plugintools.BuildContext{})
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

func TestBuildSandboxCoreTools_NoHostReturnsNil(t *testing.T) {
	session := fakeRunnerSession("docker", nil)
	got := buildSandboxCoreTools(session, plugintools.BuildContext{})
	if got != nil {
		t.Fatalf("expected nil core tools without host, got %v", got)
	}
}

type fakeHost struct{}

func (f *fakeHost) ReadFile(ctx context.Context, path string, offset, limit int) (sandbox.ReadResult, error) {
	return sandbox.ReadResult{Content: []byte("hello")}, nil
}

func (f *fakeHost) WriteFile(ctx context.Context, path string, content []byte) (sandbox.WriteResult, error) {
	return sandbox.WriteResult{BytesWritten: len(content)}, nil
}

func (f *fakeHost) EditFile(ctx context.Context, path string, edits []sandbox.Edit) (sandbox.EditResult, error) {
	return sandbox.EditResult{AppliedEdits: len(edits)}, nil
}

func (f *fakeHost) Stat(ctx context.Context, path string) (sandbox.StatResult, error) {
	return sandbox.StatResult{}, nil
}

func (f *fakeHost) ListDir(ctx context.Context, path string) ([]sandbox.DirEntry, error) {
	return nil, nil
}
func (f *fakeHost) MkdirAll(ctx context.Context, path string, perm uint32) error  { return nil }
func (f *fakeHost) Remove(ctx context.Context, path string, recursive bool) error { return nil }
func (f *fakeHost) Rename(ctx context.Context, oldPath, newPath string) error     { return nil }
func (f *fakeHost) CreateTemp(ctx context.Context, dir, pattern string) (sandbox.TempFile, error) {
	return nil, nil
}

func (f *fakeHost) Exec(ctx context.Context, command string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (f *fakeHost) StartProcess(ctx context.Context, req sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	return nil, nil
}

func (f *fakeHost) ResolvePath(path string) (string, error) { return path, nil }
func (f *fakeHost) WorkingDir() string                      { return "/tmp" }
