package skills

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type projectTestSession struct {
	sandbox.Session
	filesystem sandbox.Filesystem
	err        error
	calls      int
}

func (s *projectTestSession) WorkingDir() string { return sandbox.PathWorkspace + "/project" }
func (s *projectTestSession) Filesystem() (sandbox.Filesystem, error) {
	s.calls++
	return s.filesystem, s.err
}

type closeCountingFS struct {
	sandbox.Filesystem
	closes   int
	closeErr error
}

func (f *closeCountingFS) Close() error { f.closes++; return f.closeErr }

type malformedProjectFS struct {
	sandbox.Filesystem
	entries []sandbox.DirEntry
	reader  io.ReadCloser
	info    sandbox.FileInfo
	listErr error
}

func (f malformedProjectFS) List(context.Context, string) ([]sandbox.DirEntry, error) {
	return f.entries, f.listErr
}

func (f malformedProjectFS) Read(context.Context, string, sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	return f.reader, f.info, nil
}

func TestFilesystemProjectSourceDiscoversNestedSkillsAtCanonicalPaths(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		".agents/skills/outer/inner/SKILL.md": "---\nname: inner\ndescription: nested\n---\nbody",
		".agents/skills/direct/SKILL.md":      "---\nname: direct\ndescription: direct\n---\ndirect body",
	} {
		filename := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := filesystem.Close(); err != nil {
			t.Errorf("close filesystem: %v", err)
		}
	})
	source, err := newFilesystemProjectSource(filesystem, sandbox.PathWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	skills, dirs, err := source.list(context.Background())
	if err != nil || len(skills) != 2 {
		t.Fatalf("list = %#v, %v", skills, err)
	}
	if dirs["inner"] != "/workspace/.agents/skills/outer/inner" {
		t.Fatalf("inner dir = %q", dirs["inner"])
	}
	content, err := source.load(context.Background(), dirs["inner"], "SKILL.md")
	if err != nil || content == "" {
		t.Fatalf("load = %q, %v", content, err)
	}
	for _, bad := range []string{"/etc/passwd", "../SKILL.md", `a\\b`, "a\x00b"} {
		if _, err := source.load(context.Background(), dirs["inner"], bad); err == nil {
			t.Fatalf("accepted malicious path %q", bad)
		}
	}
}

func TestFilesystemProjectSourceRejectsInconsistentEntriesAndReadFailures(t *testing.T) {
	for _, entry := range []sandbox.DirEntry{
		{Name: "regular-as-dir", IsDir: true, Mode: 0o644},
		{Name: "dir-as-file", IsDir: false, Mode: fs.ModeDir | 0o755},
		{Name: "link", IsDir: false, Mode: fs.ModeSymlink},
		{Name: "pipe", IsDir: false, Mode: fs.ModeNamedPipe},
	} {
		if err := validProjectEntry(entry); err == nil {
			t.Fatalf("accepted %+v", entry)
		}
	}
}

type projectFailCloseReader struct {
	io.Reader
	err error
}

func (r *projectFailCloseReader) Close() error { return r.err }

type oversizedReader struct{ remaining int64 }

func (r *oversizedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	r.remaining -= n
	return int(n), nil
}

func TestReadProjectSkillFilePreservesReadAndCloseFailures(t *testing.T) {
	closeErr := errors.New("close")
	readErr := errors.New("read")
	_, err := readProjectSkillFile(context.Background(), malformedProjectFS{reader: &projectFailCloseReader{Reader: errorReader{err: readErr}, err: closeErr}, info: sandbox.FileInfo{Mode: 0o644}}, "/workspace/project/SKILL.md")
	if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
		t.Fatalf("joined error = %v", err)
	}

	_, err = readProjectSkillFile(context.Background(), malformedProjectFS{reader: io.NopCloser(&oversizedReader{remaining: maxCatalogSkillBytes + 1}), info: sandbox.FileInfo{Mode: 0o644, Size: maxCatalogSkillBytes + 1}}, "/workspace/project/SKILL.md")
	if !errors.Is(err, sandbox.ErrReadLimit) {
		t.Fatalf("oversize = %v", err)
	}

	_, err = readProjectSkillFile(context.Background(), malformedProjectFS{reader: io.NopCloser(strings.NewReader("short")), info: sandbox.FileInfo{Mode: 0o644, Size: 6}}, "/workspace/project/SKILL.md")
	if err == nil {
		t.Fatal("accepted length mismatch")
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestRuntimeProjectToolUsesFilesystemAndCanonicalDirectory(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "project/.agents/skills/stella/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("---\nname: stella\ndescription: project winner\n---\nproject body"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"load", "list", "search_installed"} {
		t.Run(action, func(t *testing.T) {
			base, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
			if err != nil {
				t.Fatal(err)
			}
			filesystem := &closeCountingFS{Filesystem: base}
			session := &projectTestSession{Session: sandbox.NopSession(), filesystem: filesystem}
			tool := NewTool(nil, "", "/host/must-not-be-used").WithRuntimeFilesystem(session, sandbox.PathWorkspace+"/project").WithSkillDirView(SkillDirView{Isolated: true, WorkspaceView: sandbox.PathWorkspace}).WithActionsOnly("load", "list", "search_installed")
			args := map[string]any{"action": action}
			if action == "load" {
				args["name"] = "stella"
			}
			if action == "search_installed" {
				args["query"] = "stella"
			}
			out, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if filesystem.closes != 1 || session.calls != 1 {
				t.Fatalf("filesystem calls/closes = %d/%d", session.calls, filesystem.closes)
			}
			if action == "load" && (!strings.Contains(out, "<skill_dir>/workspace/project/.agents/skills/stella</skill_dir>") || !strings.Contains(out, "project body")) {
				t.Fatalf("load = %q", out)
			}
		})
	}
}

func TestSkillDirViewProjectDirectory(t *testing.T) {
	canonical := "/workspace/project/.agents/skills/example"
	for _, tt := range []struct {
		name string
		view SkillDirView
		want string
	}{
		{"isolated", SkillDirView{Isolated: true, WorkspaceView: "/workspace"}, canonical},
		{"nonisolated", SkillDirView{WorkspaceView: "/host/workspace"}, "/host/workspace/project/.agents/skills/example"},
		{"isolated-colon", SkillDirView{Isolated: true, WorkspaceView: "/workspace"}, "/workspace/projects/a:b/.agents/skills/example"},
		{"nonisolated-colon", SkillDirView{WorkspaceView: "/host/workspace"}, "/host/workspace/projects/a:b/.agents/skills/example"},
		{"missing", SkillDirView{}, ""},
		{"bad-isolated", SkillDirView{Isolated: true, WorkspaceView: "/host/workspace"}, ""},
	} {
		input := canonical
		if strings.Contains(tt.name, "colon") {
			input = "/workspace/projects/a:b/.agents/skills/example"
		}
		if got := tt.view.projectDirectory(input); got != tt.want {
			t.Fatalf("%s = %q, want %q", tt.name, got, tt.want)
		}
	}
	for _, bad := range []string{"/workspace", "/user/project", "/workspace/../escape", "/workspace/project\\bad"} {
		if got := (SkillDirView{Isolated: true, WorkspaceView: "/workspace"}).projectDirectory(bad); got != "" {
			t.Fatalf("accepted %q as %q", bad, got)
		}
	}
}

func TestRuntimeProjectToolFailsClosedWithoutFilesystemAndSkipsHostWhenUnconfigured(t *testing.T) {
	host := t.TempDir()
	filename := filepath.Join(host, ".agents/skills/host-only/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("---\nname: host-only\ndescription: host\n---\nhost"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, filesystem := range []sandbox.Filesystem{nil} {
		session := &projectTestSession{Session: sandbox.NopSession(), filesystem: filesystem}
		tool := NewTool(nil, "", host).WithRuntimeFilesystem(session, sandbox.PathWorkspace+"/project")
		if _, err := tool.Execute(context.Background(), map[string]any{"action": "load", "name": "host-only"}); err == nil {
			t.Fatal("nil filesystem reached host fallback")
		}
	}
	errSession := &projectTestSession{Session: sandbox.NopSession(), err: errors.New("acquire")}
	errTool := NewTool(nil, "", host).WithRuntimeFilesystem(errSession, sandbox.PathWorkspace+"/project")
	if _, err := errTool.Execute(context.Background(), map[string]any{"action": "load", "name": "host-only"}); err == nil {
		t.Fatal("acquisition error reached host fallback")
	}
	session := &projectTestSession{Session: sandbox.NopSession()}
	tool := NewTool(nil, "", host).WithRuntimeFilesystem(session, "")
	if _, err := tool.Execute(WithProjectRoot(context.Background(), host), map[string]any{"action": "load", "name": "host-only"}); err == nil {
		t.Fatal("host project was consulted with runtime injection")
	}
	if session.calls != 0 {
		t.Fatalf("unconfigured project acquired filesystem %d times", session.calls)
	}
}

func TestRuntimeProjectToolPropagatesFilesystemCloseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "project/.agents/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	base, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("filesystem close")
	filesystem := &closeCountingFS{Filesystem: base, closeErr: closeErr}
	session := &projectTestSession{Session: sandbox.NopSession(), filesystem: filesystem}
	tool := NewTool(nil, "", "").WithRuntimeFilesystem(session, sandbox.PathWorkspace+"/project")
	if _, err := tool.Execute(context.Background(), map[string]any{"action": "list"}); !errors.Is(err, closeErr) {
		t.Fatalf("list error = %v", err)
	}
	if filesystem.closes != 1 {
		t.Fatalf("close count = %d", filesystem.closes)
	}
}

func TestRuntimeProjectToolJoinsCallbackAndCloseErrors(t *testing.T) {
	listErr, closeErr := errors.New("list"), errors.New("close")
	filesystem := &closeCountingFS{Filesystem: malformedProjectFS{listErr: listErr}, closeErr: closeErr}
	session := &projectTestSession{Session: sandbox.NopSession(), filesystem: filesystem}
	tool := NewTool(nil, "", "").WithRuntimeFilesystem(session, sandbox.PathWorkspace+"/project")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if !errors.Is(err, listErr) || !errors.Is(err, closeErr) {
		t.Fatalf("joined error = %v", err)
	}
	if filesystem.closes != 1 {
		t.Fatalf("close count = %d", filesystem.closes)
	}
}
