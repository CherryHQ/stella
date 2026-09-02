package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/webfetch"
	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// passthroughHost is a minimal sandbox.Session that executes commands directly.
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

func (h *passthroughHost) Policy() sandbox.Policy    { return sandbox.Policy{} }
func (h *passthroughHost) Files() sandbox.FileAccess { return passthroughFiles{} }
func (h *passthroughHost) WorkingDir() string        { return h.workDir }

type passthroughFiles struct{}

func (passthroughFiles) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (passthroughFiles) ReadDir(name string) ([]sandbox.DirEntry, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil {
			out = append(out, sandbox.DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
		}
	}
	return out, nil
}

func (passthroughFiles) Stat(name string) (sandbox.FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return sandbox.FileInfo{}, err
	}
	return sandbox.FileInfo{IsDir: info.IsDir(), Size: info.Size()}, nil
}

func (passthroughFiles) WriteFile(name string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, content, mode)
}
func (passthroughFiles) ProjectFiles(string, []sandbox.ProjectedFile) error { return fs.ErrPermission }
func (passthroughFiles) ProjectTempFiles(string, []sandbox.ProjectedFile) (string, error) {
	return "", fs.ErrPermission
}

func TestDirectToolRegistryRegistersBashAndWebFetch(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())

	workDir := t.TempDir()
	host := &passthroughHost{workDir: workDir}
	reg := pkgtools.NewRegistry()
	for _, tool := range agentsandbox.NewTools(host, nil, nil) {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Definition().Name, err)
		}
	}
	if err := reg.Register(webfetch.NewTool(webfetch.ActionTools()[0])); err != nil {
		t.Fatalf("register web_fetch: %v", err)
	}
	defer func() { _ = reg.Close() }()

	bashResult, err := reg.Execute(context.Background(), "bash", map[string]any{"command": "pwd -P"})
	if err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(workDir)
	if !strings.Contains(bashResult, resolved) {
		t.Fatalf("bash result = %q, want work dir %q", bashResult, resolved)
	}

	if !reg.Has("web_fetch") {
		t.Fatal("web_fetch is not registered")
	}
}
