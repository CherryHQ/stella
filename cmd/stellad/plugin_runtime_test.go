package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
	"github.com/CherryHQ/stella/plugins/tools/webfetch"
)

func newTestHTTPServer(t *testing.T, handler http.Handler) (srv *httptest.Server) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Skipf("local test server unavailable: %v", r)
		}
	}()

	if port := os.Getenv("PORT"); port != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Skipf("listen on PORT=%q: %v", port, err)
		}
		srv = httptest.NewUnstartedServer(handler)
		srv.Listener = ln
		srv.Start()
		return srv
	}

	return httptest.NewServer(handler)
}

// passthroughHost is a minimal sandbox.Host that executes commands directly.
type passthroughHost struct {
	sandbox.Session
	workDir string
}

func (h *passthroughHost) Exec(_ context.Context, command string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	cwd := h.workDir
	if opts.Cwd != "" {
		cwd = opts.Cwd
	}
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = cwd
	for k, v := range opts.Env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return sandbox.ExecResult{}, err
		}
	}
	return sandbox.ExecResult{Stdout: string(out), ExitCode: exitCode}, nil
}

func (h *passthroughHost) WorkingDir() string { return h.workDir }

// Filesystem mounts the host work directory at the canonical workspace root, so
// read/write/edit address files by canonical path while bash still runs in the
// real working directory.
func (h *passthroughHost) Filesystem() (sandbox.Filesystem, error) {
	return fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: h.workDir}})
}

func TestDirectToolRegistryExecuteReadWriteEdit(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := &passthroughHost{workDir: dir}
	reg := pkgtools.NewRegistry()
	for _, tool := range agentsandbox.NewTools(host, "", nil) {
		reg.Register(tool)
	}
	defer func() { _ = reg.Close() }()

	readResult, err := reg.Execute(context.Background(), "read", map[string]any{"path": "/workspace/note.txt"})
	if err != nil {
		t.Fatalf("read execute: %v", err)
	}
	if !strings.Contains(readResult, "hello world") {
		t.Fatalf("read result = %q, want file content", readResult)
	}

	editResult, err := reg.Execute(context.Background(), "edit", map[string]any{
		"path":       "/workspace/note.txt",
		"old_string": "world",
		"new_string": "stella",
	})
	if err != nil {
		t.Fatalf("edit execute: %v", err)
	}
	if editResult == "" {
		t.Fatal("expected non-empty edit result")
	}

	updated, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "hello stella" {
		t.Fatalf("updated file = %q, want %q", string(updated), "hello stella")
	}

	writeResult, err := reg.Execute(context.Background(), "write", map[string]any{
		"path":    "/workspace/write.txt",
		"content": "from plugin",
	})
	if err != nil {
		t.Fatalf("write execute: %v", err)
	}
	if writeResult == "" {
		t.Fatal("expected non-empty write result")
	}

	written, err := os.ReadFile(filepath.Join(dir, "write.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "from plugin" {
		t.Fatalf("write file = %q, want %q", string(written), "from plugin")
	}
}

func TestDirectToolRegistryExecuteBashAndWebFetch(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())

	workDir := t.TempDir()
	host := &passthroughHost{workDir: workDir}
	reg := pkgtools.NewRegistry()
	for _, tool := range agentsandbox.NewTools(host, "", nil) {
		reg.Register(tool)
	}
	reg.Register(webfetch.New())
	defer func() { _ = reg.Close() }()

	bashResult, err := reg.Execute(context.Background(), "bash", map[string]any{"command": "pwd -P"})
	if err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(workDir)
	if !strings.Contains(bashResult, resolved) {
		t.Fatalf("bash result = %q, want work dir %q", bashResult, resolved)
	}

	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><main><h1>Plugin Fetch</h1><p>ok</p></main></body></html>"))
	}))
	defer srv.Close()

	fetchResult, err := reg.Execute(context.Background(), "webfetch", map[string]any{
		"url":    srv.URL,
		"format": "text",
	})
	if err != nil {
		t.Fatalf("webfetch execute: %v", err)
	}
	if !strings.Contains(fetchResult, "Plugin Fetch") {
		t.Fatalf("webfetch result = %q, want fetched content", fetchResult)
	}
}
