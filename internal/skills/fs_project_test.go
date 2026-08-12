package skills

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/plugins"
)

type snapshotRoot struct{ fsys fstest.MapFS }

func projectSnapshotFromDisk(t *testing.T, root string) *ProjectSnapshot {
	t.Helper()
	files := fstest.MapFS{}
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotProjectSkills(t.Context(), snapshotRoot{files}, ".")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (r snapshotRoot) Close() error { return nil }
func (r snapshotRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return fs.Stat(r.fsys, name)
}

func (r snapshotRoot) List(_ context.Context, name string, o home.ListOptions) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(r.fsys, name)
	if err != nil {
		return nil, err
	}
	if len(entries) > o.Limit {
		return nil, home.ErrListLimit
	}
	return entries, nil
}

func (r snapshotRoot) Read(_ context.Context, name string, dst io.Writer, o home.ReadOptions) error {
	b, err := fs.ReadFile(r.fsys, name)
	if err != nil {
		return err
	}
	if int64(len(b)) >= o.MaxBytes {
		return home.ErrReadLimit
	}
	_, err = dst.Write(b)
	return err
}

func (snapshotRoot) Write(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.ErrUnsupported
}

func (snapshotRoot) Upload(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.ErrUnsupported
}

func (snapshotRoot) Mkdir(context.Context, string, fs.FileMode, home.MkdirOptions) error {
	return errors.ErrUnsupported
}

func (snapshotRoot) Remove(context.Context, string, home.RemoveOptions) error {
	return errors.ErrUnsupported
}

func (snapshotRoot) Rename(context.Context, string, string, home.RenameOptions) error {
	return errors.ErrUnsupported
}

func TestProjectSnapshotLogicalSubprojectLoadsWithoutHostPath(t *testing.T) {
	root := snapshotRoot{fstest.MapFS{
		"projects/app/.agents/skills/deploy/SKILL.md":          {Data: []byte("---\nname: deploy\ndescription: deploy app\n---\nbody")},
		"projects/app/.agents/skills/deploy/references/api.md": {Data: []byte("api")},
	}}
	snapshot, err := SnapshotProjectSkills(context.Background(), root, "projects/app")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProjectSnapshot(context.Background(), snapshot)
	content, dir, resolved, err := NewService(nil, t.TempDir()).LoadFile(ctx, "deploy", "references/api.md", plugins.SkillViewContext{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if content != "api" || resolved.Scope != "project" || dir != "/workspace/projects/app/.agents/skills/deploy" {
		t.Fatalf("content=%q dir=%q resolved=%#v", content, dir, resolved)
	}
}

func TestProjectSnapshotRejectsTraversal(t *testing.T) {
	for _, projectPath := range []string{"../escape", "projects/../escape", "/escape", `projects\escape`} {
		if _, err := SnapshotProjectSkills(context.Background(), snapshotRoot{fstest.MapFS{}}, projectPath); err == nil {
			t.Fatalf("SnapshotProjectSkills(%q) succeeded", projectPath)
		}
	}
}

func TestProjectSnapshotFileCountBound(t *testing.T) {
	files := fstest.MapFS{".agents/skills/demo/SKILL.md": {Data: []byte("---\nname: demo\ndescription: demo\n---")}}
	for i := range ProjectSnapshotMaxFiles {
		files[".agents/skills/demo/file-"+string(rune(0x1000+i))] = &fstest.MapFile{Data: []byte("x")}
	}
	_, err := SnapshotProjectSkills(context.Background(), snapshotRoot{files}, ".")
	if !errors.Is(err, ErrProjectSnapshotLimit) {
		t.Fatalf("error=%v, want ErrProjectSnapshotLimit", err)
	}
}
