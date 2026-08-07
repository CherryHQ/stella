package prompt_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type contextFilesystemSession struct {
	sandbox.Session
	root string
}

func (s contextFilesystemSession) WorkingDir() string { return "/workspace/project/app" }
func (s contextFilesystemSession) FilesystemWorkingDirectory() (string, bool) {
	return s.WorkingDir(), true
}

func (s contextFilesystemSession) Filesystem() (sandbox.Filesystem, error) {
	return fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: s.root}})
}

func TestBuildSystemPromptLoadsProjectContextWithoutInjectedHost(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		ProjectRoot:  project,
	})
	if !strings.Contains(p, "root instructions") {
		t.Fatalf("expected root AGENTS.md content in prompt: %s", p)
	}
	if !strings.Contains(p, "project instructions") {
		t.Fatalf("expected project AGENTS.md content in prompt: %s", p)
	}
}

func TestBuildSystemPromptLoadsActiveContextFromFilesystem(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "agents.MD"), []byte("project instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", ProjectRoot: "/a/host/path/must/not/be/read", Host: contextFilesystemSession{Session: sandbox.NopSession(), root: root}})
	if !strings.Contains(p, "root instructions") || !strings.Contains(p, "project instructions") {
		t.Fatalf("filesystem context missing: %s", p)
	}
	if strings.Index(p, "root instructions") > strings.Index(p, "project instructions") {
		t.Fatal("ancestor context ordering changed")
	}
}

type brokenContextSession struct{ sandbox.Session }

func (brokenContextSession) WorkingDir() string { return "/workspace" }
func (s brokenContextSession) FilesystemWorkingDirectory() (string, bool) {
	return s.WorkingDir(), true
}

func (brokenContextSession) Filesystem() (sandbox.Filesystem, error) {
	return nil, errors.New("provider unavailable")
}

func TestBuildSystemPromptDoesNotFallbackAfterInjectedFilesystemFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("host secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", ProjectRoot: root, Host: brokenContextSession{Session: sandbox.NopSession()}})
	if strings.Contains(p, "host secret") {
		t.Fatal("injected filesystem failure fell back to host path")
	}
}

type nilFilesystemContextSession struct{ sandbox.Session }

func (nilFilesystemContextSession) WorkingDir() string { return "/workspace" }
func (s nilFilesystemContextSession) FilesystemWorkingDirectory() (string, bool) {
	return s.WorkingDir(), true
}

func (nilFilesystemContextSession) Filesystem() (sandbox.Filesystem, error) {
	return nil, nil
}

func TestBuildSystemPromptRejectsNilInjectedFilesystem(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("host secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A nil Filesystem with a nil error must neither panic nor fall back to host
	// I/O; the injected session stays authoritative.
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", ProjectRoot: root, Host: nilFilesystemContextSession{Session: sandbox.NopSession()}})
	if strings.Contains(p, "host secret") {
		t.Fatal("nil injected filesystem fell back to host path")
	}
}

func TestBuildSystemPromptSkipsOversizedContextFile(t *testing.T) {
	root := t.TempDir()
	oversized := append([]byte("OVERSIZED_MARKER\n"), make([]byte, 256*1024)...)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", Host: brokenSizeSession{Session: sandbox.NopSession(), root: root}})
	if strings.Contains(p, "OVERSIZED_MARKER") {
		t.Fatal("context file above the 256 KiB bound was injected")
	}
}

type brokenSizeSession struct {
	sandbox.Session
	root string
}

func (s brokenSizeSession) WorkingDir() string { return "/workspace" }
func (s brokenSizeSession) FilesystemWorkingDirectory() (string, bool) {
	return s.WorkingDir(), true
}

func (s brokenSizeSession) Filesystem() (sandbox.Filesystem, error) {
	return fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: s.root}})
}

type closeCountingFilesystem struct {
	sandbox.Filesystem
	closed *int
}

func (c closeCountingFilesystem) Close() error {
	*c.closed++
	return c.Filesystem.Close()
}

type closeSpySession struct {
	sandbox.Session
	root   string
	closed *int
}

func (s closeSpySession) WorkingDir() string { return "/workspace" }
func (s closeSpySession) FilesystemWorkingDirectory() (string, bool) {
	return s.WorkingDir(), true
}

func (s closeSpySession) Filesystem() (sandbox.Filesystem, error) {
	fs, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: s.root}})
	if err != nil {
		return nil, err
	}
	return closeCountingFilesystem{Filesystem: fs, closed: s.closed}, nil
}

func TestBuildSystemPromptClosesInjectedFilesystem(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	closed := 0
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", Host: closeSpySession{Session: sandbox.NopSession(), root: root, closed: &closed}})
	if !strings.Contains(p, "root instructions") {
		t.Fatalf("expected injected context in prompt: %s", p)
	}
	if closed != 1 {
		t.Fatalf("injected filesystem closed %d times, want exactly 1", closed)
	}
}
