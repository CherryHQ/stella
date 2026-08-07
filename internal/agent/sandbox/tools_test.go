package sandbox

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/fsops"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// toolTestSession is a Filesystem-backed session: read/write/edit resolve a
// canonical sandbox path and operate through fsops mounts confined to real
// temp directories. Nothing is addressed by host coordinates.
type toolTestSession struct {
	pkgsandbox.Session
	policy  pkgsandbox.Policy
	workDir string
	mounts  []fsops.Mount
}

func (s *toolTestSession) Policy() pkgsandbox.Policy { return s.policy }
func (s *toolTestSession) WorkingDir() string {
	if s.workDir == "" {
		return pkgsandbox.PathWorkspace
	}
	return s.workDir
}

func (s *toolTestSession) Filesystem() (pkgsandbox.Filesystem, error) {
	return fsops.NewFilesystem(s.mounts)
}

func (s *toolTestSession) ProjectFilesystemPath(input string) (string, bool) {
	if pkgsandbox.IsCanonicalFilesystemPath(input) {
		return input, true
	}
	for _, mount := range s.mounts {
		if input == mount.Path || strings.HasPrefix(input, mount.Path+"/") {
			return input, true
		}
		rel, err := filepath.Rel(mount.Directory, input)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			canonical := path.Join(mount.Path, filepath.ToSlash(rel))
			return canonical, pkgsandbox.IsCanonicalFilesystemPath(canonical)
		}
	}
	return "", false
}

func workspaceSession(t *testing.T, dir string) *toolTestSession {
	t.Helper()
	return &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace,
		mounts:  []fsops.Mount{{Path: pkgsandbox.PathWorkspace, Directory: dir}},
	}
}

// panicPolicySession fails if Policy() is read; it proves literal (non-variable)
// paths canonicalize without touching the session policy.
type panicPolicySession struct{ pkgsandbox.Session }

func (panicPolicySession) Policy() pkgsandbox.Policy {
	panic("policy must not be read for literal paths")
}
func (panicPolicySession) WorkingDir() string { return pkgsandbox.PathWorkspace }

type unprojectableSession struct {
	pkgsandbox.Session
	policy pkgsandbox.Policy
}

func (s unprojectableSession) Policy() pkgsandbox.Policy { return s.policy }
func (unprojectableSession) WorkingDir() string          { return pkgsandbox.PathWorkspace }

func TestLiteralToolPathsDoNotRequireSessionPolicy(t *testing.T) {
	session := panicPolicySession{Session: pkgsandbox.NopSession()}
	// Absolute paths canonicalize as-is; relative paths join the working dir.
	// Neither form is a leading variable, so Policy() must never be consulted.
	for input, want := range map[string]string{
		"/tmp/literal.txt":    "/tmp/literal.txt",
		"/workspace/./a/../b": "/workspace/b",
		"relative.txt":        "/workspace/relative.txt",
		"nested/relative.txt": "/workspace/nested/relative.txt",
	} {
		got, err := resolveToolPath(session, input)
		if err != nil {
			t.Fatalf("resolve literal %q: %v", input, err)
		}
		if got != want {
			t.Errorf("resolveToolPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestVariableToolPathRequiresMappedProjector(t *testing.T) {
	missing := unprojectableSession{Session: pkgsandbox.NopSession(), policy: pkgsandbox.Policy{Env: map[string]string{"HOME": "/workspace"}}}
	if _, err := resolveToolPath(missing, "$HOME/file"); err == nil {
		t.Fatal("variable path succeeded without projector")
	}
	unmapped := &toolTestSession{Session: pkgsandbox.NopSession(), policy: pkgsandbox.Policy{Env: map[string]string{"HOME": "/unmapped"}}}
	if _, err := resolveToolPath(unmapped, "$HOME/file"); err == nil {
		t.Fatal("variable path succeeded with unmapped source")
	}
}

func TestToolIntArg(t *testing.T) {
	tests := []struct {
		args map[string]any
		key  string
		def  int
		want int
	}{
		{map[string]any{"n": float64(5)}, "n", 0, 5},
		{map[string]any{"n": 7}, "n", 0, 7},
		{map[string]any{"n": int32(3)}, "n", 0, 3},
		{map[string]any{"n": int64(9)}, "n", 0, 9},
		{map[string]any{}, "n", 42, 42},
		{map[string]any{"n": "bad"}, "n", 99, 99},
	}
	for _, tc := range tests {
		got := toolIntArg(tc.args, tc.key, tc.def)
		if got != tc.want {
			t.Errorf("toolIntArg(%v, %q, %d) = %d, want %d", tc.args, tc.key, tc.def, got, tc.want)
		}
	}
}

func TestPaginateReadContent(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	tests := []struct {
		offset    int
		limit     int
		wantLines int
		wantTotal int
	}{
		{1, 0, 5, 5},
		{2, 2, 2, 5},
		{6, 0, 0, 5},
		{1, 100, 5, 5},
	}
	for _, tc := range tests {
		paged, total := paginateReadContent(content, tc.offset, tc.limit)
		if total != tc.wantTotal {
			t.Errorf("offset=%d limit=%d: total=%d want %d", tc.offset, tc.limit, total, tc.wantTotal)
		}
		if tc.wantLines == 0 {
			if paged != "" {
				t.Errorf("offset=%d limit=%d: expected empty paged, got %q", tc.offset, tc.limit, paged)
			}
			continue
		}
		lines := splitLines(paged)
		if len(lines) != tc.wantLines {
			t.Errorf("offset=%d limit=%d: got %d lines, want %d", tc.offset, tc.limit, len(lines), tc.wantLines)
		}
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestReadTool_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := newReadTool(workspaceSession(t, dir)).Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestWriteTool_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	_, err := newWriteTool(workspaceSession(t, dir)).Execute(context.Background(), map[string]any{"path": "/workspace/out.txt", "content": "data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("file content = %q, want %q", string(got), "data")
	}
}

func TestEditTool_EditsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "edit.txt"), []byte("foo bar"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := newEditTool(workspaceSession(t, dir)).Execute(context.Background(), map[string]any{"path": "edit.txt", "old_string": "foo", "new_string": "baz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "edit.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "baz bar" {
		t.Errorf("file content = %q, want %q", string(got), "baz bar")
	}
}

func TestFileToolPathDescriptionsUseSemanticRoots(t *testing.T) {
	for _, definition := range ToolDefinitions() {
		if definition.Name != "read" && definition.Name != "write" && definition.Name != "edit" {
			continue
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		path := properties["path"].(map[string]any)
		description := path["description"].(string)
		for _, want := range []string{"Relative paths are working/project files", "$HOME", "$STELLA_ASSETS_DIR", "$TMPDIR"} {
			if !strings.Contains(description, want) {
				t.Errorf("%s path description = %q, missing %q", definition.Name, description, want)
			}
		}
		for _, want := range []string{"Default work to $HOME", "final user deliverables in $STELLA_ASSETS_DIR when available"} {
			if !strings.Contains(description, want) {
				t.Errorf("%s path description = %q, missing %q", definition.Name, description, want)
			}
		}
	}
}

// TestToolPathsExpandSandboxViewAndRemainConfined exercises every leading
// variable across the three canonical roots and confirms a traversal that
// escapes a mount is rejected before any host write.
func TestToolPathsExpandSandboxViewAndRemainConfined(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	userData := filepath.Join(t.TempDir(), "user")
	tmp := filepath.Join(t.TempDir(), "tmp")
	for _, dir := range []string{workspace, filepath.Join(userData, "assets"), tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userData, "assets", "upload.txt"), []byte("uploaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "edit.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace,
		policy: pkgsandbox.Policy{Env: map[string]string{
			pkgsandbox.EnvHome:            pkgsandbox.PathWorkspace,
			pkgsandbox.EnvStellaAssetsDir: pkgsandbox.PathUser + "/assets",
			pkgsandbox.EnvTempDir:         pkgsandbox.PathTemp,
		}},
		mounts: []fsops.Mount{
			{Path: pkgsandbox.PathWorkspace, Directory: workspace},
			{Path: pkgsandbox.PathUser, Directory: userData},
			{Path: pkgsandbox.PathTemp, Directory: tmp},
		},
	}

	read, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "$STELLA_ASSETS_DIR/upload.txt"})
	if err != nil || read != "uploaded" {
		t.Fatalf("read assets = %q, %v; want uploaded", read, err)
	}
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "$HOME/output.txt", "content": "written"}); err != nil {
		t.Fatalf("write HOME: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "output.txt")); err != nil || string(got) != "written" {
		t.Fatalf("HOME output = %q, %v", got, err)
	}
	if _, err := newEditTool(session).Execute(context.Background(), map[string]any{"path": "$TMPDIR/edit.txt", "old_string": "before", "new_string": "after"}); err != nil {
		t.Fatalf("edit TMPDIR: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(tmp, "edit.txt")); err != nil || string(got) != "after" {
		t.Fatalf("TMPDIR edit = %q, %v", got, err)
	}
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "$HOME/../escape.txt", "content": "nope"}); err == nil {
		t.Fatal("write accepted traversal outside the mounted roots")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside the workspace mount: %v", err)
	}
}

func TestToolPathVariableProjectsHostAssetPathToCanonicalFilesystemPath(t *testing.T) {
	workspace := t.TempDir()
	userData := t.TempDir()
	assets := filepath.Join(userData, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "upload.txt"), []byte("uploaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace,
		policy: pkgsandbox.Policy{
			Filesystem: pkgsandbox.FilesystemPolicy{
				WorkspaceRoot: workspace,
				WorkingDir:    workspace,
				Mounts: []pkgsandbox.Mount{
					{HostPath: workspace, SandboxPath: pkgsandbox.PathWorkspace, Access: pkgsandbox.MountReadWrite},
					{HostPath: userData, SandboxPath: pkgsandbox.PathUser, Access: pkgsandbox.MountReadWrite},
				},
			},
			Env: map[string]string{pkgsandbox.EnvStellaAssetsDir: assets},
		},
		mounts: []fsops.Mount{
			{Path: pkgsandbox.PathWorkspace, Directory: workspace},
			{Path: pkgsandbox.PathUser, Directory: userData},
		},
	}

	got, err := resolveToolPath(session, "$STELLA_ASSETS_DIR/upload.txt")
	if err != nil || got != "/user/assets/upload.txt" {
		t.Fatalf("resolve variable path = %q, %v; want /user/assets/upload.txt", got, err)
	}
	read, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "$STELLA_ASSETS_DIR/upload.txt"})
	if err != nil || read != "uploaded" {
		t.Fatalf("read variable path = %q, %v; want uploaded", read, err)
	}

	if _, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": filepath.Join(assets, "upload.txt")}); err == nil {
		t.Fatal("literal host asset path was accepted by Filesystem")
	}
}

func TestToolPathVariableKeepsCanonicalSandboxPath(t *testing.T) {
	session := &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace,
		policy:  pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvStellaAssetsDir: "/user/assets"}},
	}
	got, err := resolveToolPath(session, "$STELLA_ASSETS_DIR/upload.txt")
	if err != nil || got != "/user/assets/upload.txt" {
		t.Fatalf("resolve canonical variable path = %q, %v; want /user/assets/upload.txt", got, err)
	}
}

// TestToolPathsResolveRelativeAgainstWorkingDir proves a leading variable is
// expanded to its canonical root, while a bare relative path joins the session
// working directory rather than being treated as a literal variable directory.
func TestToolPathsResolveRelativeAgainstWorkingDir(t *testing.T) {
	workspace := t.TempDir()
	session := &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace + "/project",
		policy:  pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvHome: pkgsandbox.PathWorkspace}},
		mounts:  []fsops.Mount{{Path: pkgsandbox.PathWorkspace, Directory: workspace}},
	}
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "output.txt", "content": "written"}); err != nil {
		t.Fatalf("write relative: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "project", "output.txt")); err != nil || string(got) != "written" {
		t.Fatalf("relative output = %q, %v; want working-dir join", got, err)
	}
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "$HOME/root.txt", "content": "home"}); err != nil {
		t.Fatalf("write HOME: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "root.txt")); err != nil || string(got) != "home" {
		t.Fatalf("HOME output = %q, %v; want canonical expansion", got, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "project", "$HOME")); !os.IsNotExist(err) {
		t.Fatalf("working dir has literal $HOME directory: %v", err)
	}
}

func TestToolPathsRejectInvalidLeadingVariablesBeforeWriting(t *testing.T) {
	workspace := t.TempDir()
	session := &toolTestSession{
		Session: pkgsandbox.NopSession(),
		workDir: pkgsandbox.PathWorkspace,
		policy:  pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvHome: pkgsandbox.PathWorkspace}},
		mounts:  []fsops.Mount{{Path: pkgsandbox.PathWorkspace, Directory: workspace}},
	}
	for _, path := range []string{"$UNKNOWN/file.txt", "$STELLA_ASSETS_DIR/file.txt", "${HOME"} {
		t.Run(path, func(t *testing.T) {
			if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": path, "content": "nope"}); err == nil {
				t.Fatalf("write %q succeeded", path)
			}
		})
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid paths created files: %v", entries)
	}
}
