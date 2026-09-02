package tools

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

type spillFiles struct {
	files map[string][]byte
}

func (f *spillFiles) ReadFile(name string) ([]byte, error)               { return f.files[name], nil }
func (f *spillFiles) ReadDir(string) ([]sandbox.DirEntry, error)         { return nil, nil }
func (f *spillFiles) Stat(string) (sandbox.FileInfo, error)              { return sandbox.FileInfo{}, nil }
func (f *spillFiles) WriteFile(string, []byte, fs.FileMode) error        { return nil }
func (f *spillFiles) ProjectFiles(string, []sandbox.ProjectedFile) error { return nil }
func (f *spillFiles) ProjectTempFiles(name string, files []sandbox.ProjectedFile) (string, error) {
	root := path.Join("/tmp", name)
	for _, file := range files {
		f.files[path.Join(root, file.Path)] = file.Content
	}
	return root, nil
}

func TestSpillResultProjectsFullContentAndReturnsHeadTail(t *testing.T) {
	content := "first\n" + strings.Repeat("middle\n", InlineResultBytes/3) + "last\n"
	files := &spillFiles{files: map[string][]byte{}}

	spilled, err := SpillResult(files, "webfetch", "content.txt", content)
	if err != nil {
		t.Fatal(err)
	}
	if spilled == nil || spilled.Path == "" {
		t.Fatalf("SpillResult() = %#v, want file projection", spilled)
	}
	if got := string(files.files[spilled.Path]); got != content {
		t.Fatalf("stored content differs")
	}
	if !strings.Contains(spilled.Head, "first") || !strings.Contains(spilled.Tail, "last") {
		t.Fatalf("preview does not retain head and tail: %#v", spilled)
	}
}

func TestSpillResultLeavesSmallContentInline(t *testing.T) {
	spilled, err := SpillResult(&spillFiles{files: map[string][]byte{}}, "webfetch", "content.txt", "small")
	if err != nil || spilled != nil {
		t.Fatalf("SpillResult() = %#v, %v; want no spill", spilled, err)
	}
}
