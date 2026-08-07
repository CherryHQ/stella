package hostlayout

import (
	"path/filepath"
	"testing"
)

func TestLayoutValidateAndDeepestMapping(t *testing.T) {
	root := t.TempDir()
	layout := Layout{WorkspaceSource: root, WorkingDirSource: filepath.Join(root, "project"), Mounts: []Mount{
		{Source: root, Target: "/workspace", Access: ReadWrite},
		{Source: filepath.Join(root, "project"), Target: "/project", Access: ReadOnly},
	}}
	if err := layout.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, ok := layout.SourceToTarget(filepath.Join(root, "project", "a.txt")); !ok || got != "/project/a.txt" {
		t.Fatalf("SourceToTarget = %q, %v", got, ok)
	}
}

func TestLayoutCloneDoesNotAliasMounts(t *testing.T) {
	root := t.TempDir()
	layout := Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}}
	clone := layout.Clone()
	layout.Mounts[0].Source = "/redirected"
	if clone.Mounts[0].Source == layout.Mounts[0].Source {
		t.Fatal("Clone aliases Mounts")
	}
}

func TestLayoutTargetsAreCanonicalPOSIXPaths(t *testing.T) {
	root := t.TempDir()
	layout := Layout{WorkspaceSource: root, WorkingDirSource: filepath.Join(root, "nested"), Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}}
	if err := layout.Validate(); err != nil {
		t.Fatalf("Validate() with POSIX target: %v", err)
	}
	if got, ok := layout.SourceToTarget(filepath.Join(root, "nested", "file")); !ok || got != "/workspace/nested/file" {
		t.Fatalf("SourceToTarget = %q, %v; want POSIX target", got, ok)
	}
}

func TestLayoutValidateRejectsInvalidBoundaries(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	base := Layout{WorkspaceSource: root, WorkingDirSource: inside, Mounts: []Mount{{Source: root, Target: "/workspace", Access: ReadWrite}}}
	tests := []struct {
		name   string
		mutate func(*Layout)
	}{
		{"relative target", func(l *Layout) { l.Mounts[0].Target = "workspace" }},
		{"backslash target", func(l *Layout) { l.Mounts[0].Target = `\workspace` }},
		{"noncanonical target", func(l *Layout) { l.Mounts[0].Target = "/workspace/../workspace" }},
		{"source escape", func(l *Layout) { l.WorkingDirSource = filepath.Dir(root) }},
		{"duplicate source", func(l *Layout) { l.Mounts = append(l.Mounts, Mount{Source: root, Target: "/other", Access: ReadOnly}) }},
		{"duplicate target", func(l *Layout) {
			l.Mounts = append(l.Mounts, Mount{Source: filepath.Join(root, "other"), Target: "/workspace", Access: ReadOnly})
		}},
		{"read-only workspace", func(l *Layout) { l.Mounts[0].Access = ReadOnly }},
		{"unmappable working directory", func(l *Layout) { l.WorkingDirSource = t.TempDir() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := base.Clone()
			test.mutate(&layout)
			if err := layout.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}
