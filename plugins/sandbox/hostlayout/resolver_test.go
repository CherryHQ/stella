package hostlayout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverDeepestTargetAndReadOnly(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []Mount{
		{Source: root, Target: "/workspace", Access: ReadWrite},
		{Source: nested, Target: "/workspace/nested", Access: ReadOnly},
	}}
	r := NewResolver(layout)
	got, err := r.SourceForRead("/workspace/nested/a.txt")
	if err != nil || got != filepath.Join(nested, "a.txt") {
		t.Fatalf("SourceForRead = %q, %v", got, err)
	}
	if _, err := r.SourceForWrite("/workspace/nested/a.txt"); err == nil {
		t.Fatal("write through read-only mount succeeded")
	}
}

func TestResolverRejectsMissingPathBelowSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}})
	if _, err := r.SourceForWrite("/workspace/escape/new.txt"); err == nil {
		t.Fatal("symlink parent accepted")
	}
}

func TestResolverRelativeAndPOSIXTarget(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "project")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(Layout{WorkspaceSource: root, WorkingDirSource: work, Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}})
	got, err := r.SourceForRead("file.txt")
	if err != nil || got != filepath.Join(work, "file.txt") {
		t.Fatalf("relative = %q, %v", got, err)
	}
	if target, ok := r.SourceToTarget(work); !ok || target != "/workspace/project" {
		t.Fatalf("target = %q, %v", target, ok)
	}
}

func TestResolverPhysicalAbsolutePathAndEscapeContainment(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}})

	if got, err := r.SourceForRead(inside); err != nil || got != inside {
		t.Fatalf("physical absolute path = %q, %v; want %q", got, err, inside)
	}
	for _, input := range []string{"/workspace/../outside", filepath.Join(outside, "secret")} {
		if _, err := r.SourceForRead(input); err == nil {
			t.Errorf("SourceForRead(%q) accepted an escape", input)
		}
	}
}

func TestResolverMissingLeafAndDeepestSourceMount(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []Mount{
		{Source: root, Target: "/workspace", Access: ReadWrite},
		{Source: nested, Target: "/user", Access: ReadWrite},
	}})

	missing := filepath.Join(nested, "new", "file")
	if got, err := r.SourceForWrite(missing); err != nil || got != missing {
		t.Fatalf("missing leaf = %q, %v; want %q", got, err, missing)
	}
	if got, ok := r.SourceToTarget(filepath.Join(nested, "file")); !ok || got != "/user/file" {
		t.Fatalf("deepest source mapping = %q, %v; want /user/file", got, ok)
	}
}
