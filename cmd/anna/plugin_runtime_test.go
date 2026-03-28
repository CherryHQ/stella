package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/config"
)

var (
	annaBinaryOnce sync.Once
	annaBinaryPath string
	annaBinaryErr  error
)

func buildAnnaBinary(t *testing.T) string {
	t.Helper()

	annaBinaryOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			annaBinaryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "anna-plugin-bin-")
		if err != nil {
			annaBinaryErr = err
			return
		}
		binPath := filepath.Join(dir, "anna-test-bin")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/anna")
		cmd.Dir = root
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			annaBinaryErr = fmt.Errorf("build anna binary: %w: %s", err, string(out))
			return
		}
		annaBinaryPath = binPath
	})
	if annaBinaryErr != nil {
		t.Fatal(annaBinaryErr)
	}
	return annaBinaryPath
}

func TestPluginToolRegistryExecuteReadWriteEdit(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	t.Setenv("ANNA_PLUGIN_ENTRYPOINT", buildAnnaBinary(t))

	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := agenttool.NewRegistry("")
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

func TestPluginToolRegistryExecuteBashAndWebFetch(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	t.Setenv("ANNA_PLUGIN_ENTRYPOINT", buildAnnaBinary(t))

	workDir := t.TempDir()
	reg := agenttool.NewRegistry(workDir)
	defer func() { _ = reg.Close() }()

	bashResult, err := reg.Execute(context.Background(), "bash", map[string]any{"command": "pwd -P"})
	if err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	if !strings.Contains(bashResult, workDir) {
		t.Fatalf("bash result = %q, want work dir %q", bashResult, workDir)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestPluginToolRegistrySandbox(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	t.Setenv("ANNA_PLUGIN_ENTRYPOINT", buildAnnaBinary(t))

	allowed := t.TempDir()
	outside := t.TempDir()

	reg := agenttool.NewRegistry("", allowed)
	defer func() { _ = reg.Close() }()

	_, err := reg.Execute(context.Background(), "read", map[string]any{
		"file_path": filepath.Join(outside, "secret.txt"),
	})
	if err == nil {
		t.Fatal("expected sandbox error")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("sandbox error = %v, want sandbox in error", err)
	}
}
