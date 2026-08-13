package sandbox

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// mockSession is a controllable Session for testing ResilientSession.
type mockSession struct {
	mu             sync.Mutex
	alive          bool
	closed         bool
	policy         Policy
	workingDir     string
	execCount      int
	lastExecEnv    map[string]string
	lastProcessEnv map[string]string
	done           chan struct{}
	files          FileAccess
}

func newMockSession() *mockSession {
	return &mockSession{alive: true, workingDir: "/workspace", done: make(chan struct{})}
}

func (m *mockSession) Policy() Policy        { return m.policy }
func (m *mockSession) WorkingDir() string    { return m.workingDir }
func (m *mockSession) Done() <-chan struct{} { return m.done }

func (m *mockSession) Alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

func (m *mockSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alive = false
	m.closed = true
	return nil
}

func (m *mockSession) Exec(_ context.Context, _ string, opts ExecOptions) (ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCount++
	m.lastExecEnv = maps.Clone(opts.Env)
	return ExecResult{Stdout: "ok"}, nil
}

func (m *mockSession) StartProcess(_ context.Context, req ProcessRequest) (ProcessHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastProcessEnv = maps.Clone(req.Env)
	return nil, nil
}

func (m *mockSession) Files() FileAccess {
	if m.files != nil {
		return m.files
	}
	return directTestFileAccess{}
}

type directTestFileAccess struct{}

func (directTestFileAccess) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (directTestFileAccess) ReadDir(name string) ([]DirEntry, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil {
			out = append(out, DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
		}
	}
	return out, nil
}

func (directTestFileAccess) Stat(name string) (FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{IsDir: info.IsDir(), Size: info.Size()}, nil
}

func (directTestFileAccess) WriteFile(name string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, content, mode)
}

func (directTestFileAccess) ProjectFiles(name string, files []ProjectedFile) error {
	for _, file := range files {
		if err := (directTestFileAccess{}).WriteFile(filepath.Join(name, filepath.FromSlash(file.Path)), file.Content, file.Mode); err != nil {
			return err
		}
	}
	return nil
}

type rootedTestAccess struct{ root string }

func (a rootedTestAccess) resolve(name string) string { return filepath.Join(a.root, name) }
func (a rootedTestAccess) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(a.resolve(name))
}

func (a rootedTestAccess) ReadDir(name string) ([]DirEntry, error) {
	return directTestFileAccess{}.ReadDir(a.resolve(name))
}

func (a rootedTestAccess) Stat(name string) (FileInfo, error) {
	return directTestFileAccess{}.Stat(a.resolve(name))
}

func (a rootedTestAccess) WriteFile(name string, content []byte, mode os.FileMode) error {
	return directTestFileAccess{}.WriteFile(a.resolve(name), content, mode)
}

func (a rootedTestAccess) ProjectFiles(name string, files []ProjectedFile) error {
	root := a.resolve(name)
	for _, file := range files {
		if err := (directTestFileAccess{}).WriteFile(filepath.Join(root, filepath.FromSlash(file.Path)), file.Content, file.Mode); err != nil {
			return err
		}
	}
	return nil
}

func TestResilientSession_ExecUsesExistingSession(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate")
		return nil, nil
	})

	result, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	if result.Stdout != "ok" {
		t.Errorf("got %q, want %q", result.Stdout, "ok")
	}
}

func TestResilientSessionRefreshEnvOverlaysSubsequentProcesses(t *testing.T) {
	inner := newMockSession()
	rs := NewResilientSession(inner, nil)
	rs.RefreshEnv(map[string]string{"TOKEN": "new", "CALLER": "overlay"})

	if _, err := rs.Exec(context.Background(), "true", ExecOptions{Env: map[string]string{"CALLER": "exec"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := inner.lastExecEnv["TOKEN"]; got != "new" {
		t.Fatalf("Exec TOKEN = %q, want new", got)
	}
	if got := inner.lastExecEnv["CALLER"]; got != "exec" {
		t.Fatalf("per-exec env must win; CALLER = %q", got)
	}

	if _, err := rs.StartProcess(context.Background(), ProcessRequest{}); err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	if got := inner.lastProcessEnv["TOKEN"]; got != "new" {
		t.Fatalf("StartProcess TOKEN = %q, want new", got)
	}
	if got := rs.Policy().Env["TOKEN"]; got != "new" {
		t.Fatalf("Policy TOKEN = %q, want new", got)
	}
}

func TestResilientSession_RecreatesAfterClose(t *testing.T) {
	first := newMockSession()
	second := newMockSession()

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		createCount.Add(1)
		return second, nil
	})
	rs.RefreshEnv(map[string]string{"TOKEN": "old-overlay"})

	// Close the underlying session (simulates reaper). The recreated session is
	// built from current state, so the dead session's overlay must be discarded.
	_ = first.Close()

	result, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	if result.Stdout != "ok" {
		t.Errorf("got %q, want %q", result.Stdout, "ok")
	}
	if createCount.Load() != 1 {
		t.Errorf("create called %d times, want 1", createCount.Load())
	}
	if _, ok := second.lastExecEnv["TOKEN"]; ok {
		t.Fatal("recreated session inherited a stale env overlay")
	}

	// Second exec should reuse the recreated session.
	_, err = rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("second Exec error: %v", err)
	}
	if createCount.Load() != 1 {
		t.Errorf("create called %d times after second exec, want 1", createCount.Load())
	}
}

func TestResilientSession_PermanentCloseRejectsExec(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate after permanent close")
		return nil, nil
	})

	_ = rs.Close()

	_, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err == nil {
		t.Fatal("Exec should fail after permanent Close")
	}
}

func TestResilientSessionPermanentCloseRejectsFileAccess(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, nil)
	file := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(file, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Files().ReadFile(file); err == nil {
		t.Fatal("file access succeeded after permanent Close")
	}
}

func TestResilientSessionFileAccessRecreatesDeadInner(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	first := newMockSession()
	first.files = rootedTestAccess{root: firstRoot}
	second := newMockSession()
	second.files = rootedTestAccess{root: secondRoot}
	if err := os.WriteFile(filepath.Join(firstRoot, "value"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "value"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(context.Context) (Session, error) {
		createCount.Add(1)
		return second, nil
	})
	files := rs.Files()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if content, err := files.ReadFile("value"); err != nil || string(content) != "new" {
		t.Fatalf("recreated read = %q, %v", content, err)
	}
	if err := files.WriteFile("written", []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.ProjectFiles("projection", []ProjectedFile{{Path: "SKILL.md", Content: []byte("second projection"), Mode: 0o444}}); err != nil {
		t.Fatal(err)
	}
	if createCount.Load() != 1 {
		t.Fatalf("create called %d times, want 1", createCount.Load())
	}
	for name, want := range map[string]string{
		"written":             "second",
		"projection/SKILL.md": "second projection",
	} {
		content, err := os.ReadFile(filepath.Join(secondRoot, filepath.FromSlash(name)))
		if err != nil || string(content) != want {
			t.Fatalf("second backing %s = %q, %v", name, content, err)
		}
		if _, err := os.Stat(filepath.Join(firstRoot, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dead backing received %s: %v", name, err)
		}
	}
}

func TestResilientSessionCoordinatesRecreateDeadInner(t *testing.T) {
	for _, test := range []struct {
		name string
		read func(*ResilientSession) string
		want string
	}{
		{name: "policy", read: func(session *ResilientSession) string { return session.Policy().Env[EnvTempDir] }, want: "/new/tmp"},
		{name: "working directory", read: func(session *ResilientSession) string { return session.WorkingDir() }, want: "/new/workspace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := newMockSession()
			first.policy.Env = map[string]string{EnvTempDir: "/old/tmp"}
			first.workingDir = "/old/workspace"
			second := newMockSession()
			second.policy.Env = map[string]string{EnvTempDir: "/new/tmp"}
			second.workingDir = "/new/workspace"

			var createCount atomic.Int32
			session := NewResilientSession(first, func(context.Context) (Session, error) {
				createCount.Add(1)
				return second, nil
			})
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if got := test.read(session); got != test.want {
				t.Fatalf("coordinate = %q, want %q", got, test.want)
			}
			if createCount.Load() != 1 {
				t.Fatalf("create called %d times, want 1", createCount.Load())
			}
		})
	}
}

func TestResilientSessionCoordinatesFailClosed(t *testing.T) {
	first := newMockSession()
	first.policy.Env = map[string]string{EnvTempDir: "/stale/tmp"}
	first.workingDir = "/stale/workspace"
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	session := NewResilientSession(first, func(context.Context) (Session, error) {
		return nil, errors.New("recreation failed")
	})
	if got := session.Policy().Env[EnvTempDir]; got != "" {
		t.Fatalf("failed recreation exposed stale TMPDIR %q", got)
	}
	if got := session.WorkingDir(); got != "" {
		t.Fatalf("failed recreation exposed stale working directory %q", got)
	}
}

func TestResilientSession_RecreateFailurePropagates(t *testing.T) {
	first := newMockSession()
	_ = first.Close()

	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		return nil, errors.New("docker daemon unreachable")
	})

	_, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err == nil {
		t.Fatal("Exec should fail when recreate fails")
	}
}

func TestResilientSession_AliveReflectsInner(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, nil)

	if !rs.Alive() {
		t.Error("should be alive initially")
	}

	_ = s.Close()
	if rs.Alive() {
		t.Error("should not be alive after inner close")
	}
}

func TestResilientSession_ConcurrentExecRecreatesOnce(t *testing.T) {
	first := newMockSession()
	_ = first.Close()

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		createCount.Add(1)
		return newMockSession(), nil
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, _ = rs.Exec(context.Background(), "echo hi", ExecOptions{})
		})
	}
	wg.Wait()

	if createCount.Load() != 1 {
		t.Errorf("create called %d times, want 1 (should deduplicate)", createCount.Load())
	}
}
