package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type stubHost struct {
	pkgsandbox.Session
	policy      pkgsandbox.Policy
	workingDir  string
	resolvePath func(path string) (string, error)
	files       pkgsandbox.FileAccess
}

func (s *stubHost) Policy() pkgsandbox.Policy { return s.policy }
func (s *stubHost) WorkingDir() string        { return s.workingDir }

func (s *stubHost) resolve(path string) (string, error) {
	if s.resolvePath != nil {
		return s.resolvePath(path)
	}
	return path, nil
}

func (s *stubHost) Files() pkgsandbox.FileAccess {
	if s.files != nil {
		return s.files
	}
	return stubFiles{host: s}
}

type stubFiles struct{ host *stubHost }

func (f stubFiles) ReadFile(path string) ([]byte, error) {
	resolved, err := f.host.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(resolved)
}

func (f stubFiles) ReadDir(path string) ([]pkgsandbox.DirEntry, error) {
	return nil, os.ErrPermission
}

func (f stubFiles) Stat(path string) (pkgsandbox.FileInfo, error) {
	resolved, err := f.host.resolve(path)
	if err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	return pkgsandbox.FileInfo{IsDir: info.IsDir(), Size: info.Size()}, nil
}

func (f stubFiles) WriteFile(path string, content []byte, mode os.FileMode) error {
	resolved, err := f.host.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, content, mode)
}

func (f stubFiles) ProjectFiles(string, []pkgsandbox.ProjectedFile) error {
	return os.ErrPermission
}

func (f stubFiles) ProjectTempFiles(string, []pkgsandbox.ProjectedFile) (string, error) {
	return "", os.ErrPermission
}

type toolGenerationSession struct {
	*stubHost
	alive bool
}

func (s *toolGenerationSession) Alive() bool { return s.alive }

type readCallbackFiles struct {
	pkgsandbox.FileAccess
	afterRead func()
}

func (f readCallbackFiles) ReadFile(path string) ([]byte, error) {
	content, err := f.FileAccess.ReadFile(path)
	if err == nil && f.afterRead != nil {
		f.afterRead()
	}
	return content, err
}

func tempGeneration(root, visible string) *toolGenerationSession {
	host := &stubHost{
		policy:     pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvTempDir: visible}},
		workingDir: visible,
		resolvePath: func(name string) (string, error) {
			relative, ok := pkgsandbox.POSIXPathRelative(visible, name)
			if !ok {
				return "", os.ErrPermission
			}
			return filepath.Join(root, filepath.FromSlash(relative)), nil
		},
	}
	return &toolGenerationSession{stubHost: host, alive: true}
}

func TestLiteralToolPathsDoNotRequireSessionPolicy(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "literal.txt"), "relative.txt"} {
		got, err := resolveToolExpression(nil, "", path)
		if err != nil {
			t.Fatalf("resolve literal path: %v", err)
		}
		if got != path {
			t.Errorf("resolved literal path = %q, want %q", got, path)
		}
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
	file := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(file, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{}
	tool := newReadTool(host)
	out, err := tool.Execute(context.Background(), map[string]any{"path": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestWriteTool_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "out.txt")
	host := &stubHost{}
	tool := newWriteTool(host)
	_, err := tool.Execute(context.Background(), map[string]any{"path": file, "content": "data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("file content = %q, want %q", string(got), "data")
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
	mounts := map[string]string{"/workspace": workspace, "/user": userData, "/tmp": tmp}
	host := &stubHost{
		policy: pkgsandbox.Policy{Env: map[string]string{
			pkgsandbox.EnvHome:            "/workspace",
			pkgsandbox.EnvStellaAssetsDir: "/user/assets",
			pkgsandbox.EnvTempDir:         "/tmp",
		}},
		resolvePath: func(name string) (string, error) {
			for visible, physical := range mounts {
				rel, ok := pkgsandbox.POSIXPathRelative(visible, name)
				if ok {
					return filepath.Join(physical, filepath.FromSlash(rel)), nil
				}
			}
			return "", os.ErrPermission
		},
	}

	read, err := newReadTool(host).Execute(context.Background(), map[string]any{"path": "$STELLA_ASSETS_DIR/upload.txt"})
	if err != nil || read != "uploaded" {
		t.Fatalf("read assets = %q, %v; want uploaded", read, err)
	}
	if _, err := newWriteTool(host).Execute(context.Background(), map[string]any{"path": "$HOME/output.txt", "content": "written"}); err != nil {
		t.Fatalf("write HOME: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "output.txt")); err != nil || string(got) != "written" {
		t.Fatalf("HOME output = %q, %v", got, err)
	}
	if _, err := newEditTool(host).Execute(context.Background(), map[string]any{"path": "$TMPDIR/edit.txt", "old_string": "before", "new_string": "after"}); err != nil {
		t.Fatalf("edit TMPDIR: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(tmp, "edit.txt")); err != nil || string(got) != "after" {
		t.Fatalf("TMPDIR edit = %q, %v", got, err)
	}
	if _, err := newWriteTool(host).Execute(context.Background(), map[string]any{"path": "$HOME/../escape.txt", "content": "nope"}); err == nil {
		t.Fatal("write accepted traversal outside the sandbox workspace")
	}
}

func TestFileToolsExpandPathsFromRecreatedGeneration(t *testing.T) {
	first := tempGeneration(t.TempDir(), "/old/tmp")
	first.alive = false
	secondRoot := t.TempDir()
	second := tempGeneration(secondRoot, "/new/tmp")
	createCount := 0
	session := pkgsandbox.NewResilientSession(first, func(context.Context) (pkgsandbox.Session, error) {
		createCount++
		return second, nil
	})

	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{
		"path":    "$TMPDIR/value",
		"content": "new generation",
	}); err != nil {
		t.Fatalf("write through recreated generation: %v", err)
	}
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{
		"path":    "relative",
		"content": "current working directory",
	}); err != nil {
		t.Fatalf("write relative path through recreated generation: %v", err)
	}
	if createCount != 1 {
		t.Fatalf("session recreated %d times, want 1", createCount)
	}
	content, err := os.ReadFile(filepath.Join(secondRoot, "value"))
	if err != nil || string(content) != "new generation" {
		t.Fatalf("new generation content = %q, %v", content, err)
	}
	content, err = os.ReadFile(filepath.Join(secondRoot, "relative"))
	if err != nil || string(content) != "current working directory" {
		t.Fatalf("relative new generation content = %q, %v", content, err)
	}
}

func TestEditKeepsReadAndWriteOnOneGeneration(t *testing.T) {
	firstRoot := t.TempDir()
	first := tempGeneration(firstRoot, "/tmp")
	if err := os.WriteFile(filepath.Join(firstRoot, "value"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.files = readCallbackFiles{
		FileAccess: stubFiles{host: first.stubHost},
		afterRead:  func() { first.alive = false },
	}

	secondRoot := t.TempDir()
	second := tempGeneration(secondRoot, "/tmp")
	if err := os.WriteFile(filepath.Join(secondRoot, "value"), []byte("new-generation sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	createCount := 0
	session := pkgsandbox.NewResilientSession(first, func(context.Context) (pkgsandbox.Session, error) {
		createCount++
		return second, nil
	})

	if _, err := newEditTool(session).Execute(context.Background(), map[string]any{
		"path":       "$TMPDIR/value",
		"old_string": "before",
		"new_string": "after",
	}); err != nil {
		t.Fatalf("edit first generation: %v", err)
	}
	if createCount != 0 {
		t.Fatalf("edit switched generations %d times", createCount)
	}
	if content, err := os.ReadFile(filepath.Join(firstRoot, "value")); err != nil || string(content) != "after" {
		t.Fatalf("first generation content = %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(secondRoot, "value")); err != nil || string(content) != "new-generation sentinel" {
		t.Fatalf("second generation content = %q, %v", content, err)
	}
}

func TestToolPathsExpandHostViewBeforeProjectResolution(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{
		policy:     pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvHome: workspace}},
		workingDir: project,
	}
	if _, err := newWriteTool(host).Execute(context.Background(), map[string]any{"path": "$HOME/output.txt", "content": "written"}); err != nil {
		t.Fatalf("write HOME: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "output.txt")); err != nil || string(got) != "written" {
		t.Fatalf("HOME output = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(project, "$HOME")); !os.IsNotExist(err) {
		t.Fatalf("project has literal $HOME directory: %v", err)
	}
}

func TestToolPathsRejectInvalidLeadingVariablesBeforeWriting(t *testing.T) {
	root := t.TempDir()
	host := &stubHost{policy: pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvHome: root}}}
	for _, path := range []string{"$UNKNOWN/file.txt", "$STELLA_ASSETS_DIR/file.txt", "${HOME"} {
		t.Run(path, func(t *testing.T) {
			if _, err := newWriteTool(host).Execute(context.Background(), map[string]any{"path": path, "content": "nope"}); err == nil {
				t.Fatalf("write %q succeeded", path)
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid paths created files: %v", entries)
	}
}

func TestEditTool_EditsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(file, []byte("foo bar"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{}
	tool := newEditTool(host)
	_, err := tool.Execute(context.Background(), map[string]any{"path": file, "old_string": "foo", "new_string": "baz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "baz bar" {
		t.Errorf("file content = %q, want %q", string(got), "baz bar")
	}
}
