package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/pkg/tools"
	"github.com/vaayne/anna/plugins/tools/bash"
	"github.com/vaayne/anna/plugins/tools/edit"
	"github.com/vaayne/anna/plugins/tools/read"
	"github.com/vaayne/anna/plugins/tools/webfetch"
	"github.com/vaayne/anna/plugins/tools/write"
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

func TestDirectToolRegistryExecuteReadWriteEdit(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())

	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewRegistry()
	reg.Register(&read.ReadTool{})
	reg.Register(bash.NewBashTool("", ""))
	reg.Register(&edit.EditTool{})
	reg.Register(&write.WriteTool{})
	defer func() { _ = reg.Close() }()

	readResult, err := reg.Execute(context.Background(), "read", map[string]any{"file_path": path})
	if err != nil {
		t.Fatalf("read execute: %v", err)
	}
	if !strings.Contains(readResult, "hello world") {
		t.Fatalf("read result = %q, want file content", readResult)
	}

	editResult, err := reg.Execute(context.Background(), "edit", map[string]any{
		"file_path":  path,
		"old_string": "world",
		"new_string": "anna",
	})
	if err != nil {
		t.Fatalf("edit execute: %v", err)
	}
	if editResult == "" {
		t.Fatal("expected non-empty edit result")
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "hello anna" {
		t.Fatalf("updated file = %q, want %q", string(updated), "hello anna")
	}

	writePath := filepath.Join(dir, "write.txt")
	writeResult, err := reg.Execute(context.Background(), "write", map[string]any{
		"file_path": writePath,
		"content":   "from plugin",
	})
	if err != nil {
		t.Fatalf("write execute: %v", err)
	}
	if writeResult == "" {
		t.Fatal("expected non-empty write result")
	}

	written, err := os.ReadFile(writePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "from plugin" {
		t.Fatalf("write file = %q, want %q", string(written), "from plugin")
	}
}

func TestDirectToolRegistryExecuteBashAndWebFetch(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())

	workDir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(&read.ReadTool{})
	reg.Register(bash.NewBashTool(workDir, ""))
	reg.Register(&edit.EditTool{})
	reg.Register(&write.WriteTool{})
	// Register webfetch as an extra tool (simulating enabled plugin).
	reg.Register(webfetch.New())
	defer func() { _ = reg.Close() }()

	bashResult, err := reg.Execute(context.Background(), "bash", map[string]any{"command": "pwd -P"})
	if err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	// Resolve symlinks (macOS /tmp -> /private/tmp).
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
