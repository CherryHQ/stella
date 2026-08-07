package prompt_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type promptTestFilesystem struct {
	sandbox.Filesystem
	info      sandbox.FileInfo
	reader    io.ReadCloser
	readErr   error
	paths     []string
	closeCall int
}

func (f *promptTestFilesystem) Read(_ context.Context, name string, _ sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	f.paths = append(f.paths, "read:"+name)
	return f.reader, f.info, f.readErr
}

func (f *promptTestFilesystem) Stat(_ context.Context, name string) (sandbox.FileInfo, error) {
	f.paths = append(f.paths, "stat:"+name)
	return f.info, nil
}

func (f *promptTestFilesystem) List(_ context.Context, name string) ([]sandbox.DirEntry, error) {
	f.paths = append(f.paths, "list:"+name)
	return nil, nil
}
func (f *promptTestFilesystem) Close() error { f.closeCall++; return nil }

type promptTestSession struct {
	sandbox.Session
	workingDir string
	filesystem sandbox.Filesystem
}

func (s promptTestSession) WorkingDir() string                      { return s.workingDir }
func (s promptTestSession) Filesystem() (sandbox.Filesystem, error) { return s.filesystem, nil }

type projectedPromptTestSession struct {
	promptTestSession
	projected string
	ok        bool
}

func (s projectedPromptTestSession) ProjectFilesystemPath(string) (string, bool) {
	return s.projected, s.ok
}

type closeErrorReader struct {
	io.Reader
	closes int
}

func (r *closeErrorReader) Close() error { r.closes++; return errors.New("close failed") }

type readErrorReader struct{ closes int }

func (r *readErrorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (r *readErrorReader) Close() error             { r.closes++; return nil }

func TestInjectedPromptProjectsWorkingDirAndNeverUsesDBProjectRoot(t *testing.T) {
	filesystem := &promptTestFilesystem{
		info:   sandbox.FileInfo{Size: int64(len("filesystem instructions")), Mode: 0},
		reader: io.NopCloser(strings.NewReader("filesystem instructions")),
	}
	session := sandbox.NewResilientSession(projectedPromptTestSession{
		promptTestSession: promptTestSession{Session: sandbox.NopSession(), workingDir: "/host/project", filesystem: filesystem},
		projected:         "/workspace/project",
		ok:                true,
	}, nil)
	got := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		ProjectRoot:  "/host/hostile-project-root",
		Host:         session,
	})
	if !strings.Contains(got, "filesystem instructions") {
		t.Fatalf("projected filesystem context missing: %s", got)
	}
	if strings.Contains(strings.Join(filesystem.paths, ","), "/host/") {
		t.Fatalf("injected prompt consulted host path: %v", filesystem.paths)
	}
}

func TestInjectedPromptRejectsUnprojectableWorkingDirWithoutHostFallback(t *testing.T) {
	filesystem := &promptTestFilesystem{info: sandbox.FileInfo{Mode: 0}}
	_ = prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		ProjectRoot:  "/host/secret-project",
		Host: promptTestSession{
			Session: sandbox.NopSession(), workingDir: "/host/project", filesystem: filesystem,
		},
	})
	if len(filesystem.paths) != 0 {
		t.Fatalf("unprojectable injected path used context: %v", filesystem.paths)
	}
}

func TestInjectedPromptStopsAtWorkspace(t *testing.T) {
	filesystem := &promptTestFilesystem{
		info:   sandbox.FileInfo{Size: int64(len("workspace instructions")), Mode: 0},
		reader: io.NopCloser(strings.NewReader("workspace instructions")),
	}
	got := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Host: promptTestSession{
			Session: sandbox.NopSession(), workingDir: sandbox.PathWorkspace, filesystem: filesystem,
		},
	})
	if !strings.Contains(got, "workspace instructions") {
		t.Fatal("workspace AGENTS.md was not included")
	}
	for _, request := range filesystem.paths {
		if strings.HasSuffix(request, ":/AGENTS.md") || strings.HasSuffix(request, ":/") {
			t.Fatalf("injected traversal escaped /workspace: %v", filesystem.paths)
		}
	}
}

func TestInjectedPromptRejectsInvalidFileReads(t *testing.T) {
	validInfo := sandbox.FileInfo{Size: 3, Mode: 0}
	cases := []struct {
		name   string
		info   sandbox.FileInfo
		reader io.ReadCloser
	}{
		{"nil reader", validInfo, nil},
		{"short read", sandbox.FileInfo{Size: 4, Mode: 0}, io.NopCloser(strings.NewReader("bad"))},
		{"long read", sandbox.FileInfo{Size: 2, Mode: 0}, io.NopCloser(strings.NewReader("bad"))},
		{"nonregular", sandbox.FileInfo{Size: 3, Mode: fs.ModeSymlink}, io.NopCloser(strings.NewReader("bad"))},
		{"directory", sandbox.FileInfo{Size: 3, Mode: fs.ModeDir, IsDir: true}, io.NopCloser(strings.NewReader("bad"))},
		{"read error", validInfo, &readErrorReader{}},
		{"close error", validInfo, &closeErrorReader{Reader: strings.NewReader("bad")}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			filesystem := &promptTestFilesystem{info: tt.info, reader: tt.reader}
			got := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
				SystemPrompt: "You are Stella.",
				Host:         promptTestSession{Session: sandbox.NopSession(), workingDir: sandbox.PathWorkspace, filesystem: filesystem},
			})
			if strings.Contains(got, "bad") {
				t.Fatalf("invalid injected file was included: %s", got)
			}
			if filesystem.closeCall != 1 {
				t.Fatalf("filesystem Close calls = %d, want 1", filesystem.closeCall)
			}
			switch reader := tt.reader.(type) {
			case *closeErrorReader:
				if reader.closes != 1 {
					t.Fatalf("reader Close calls = %d, want 1", reader.closes)
				}
			case *readErrorReader:
				if reader.closes != 1 {
					t.Fatalf("reader Close calls = %d, want 1", reader.closes)
				}
			}
		})
	}
}
