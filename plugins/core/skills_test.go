package core

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CherryHQ/stella/resources"
)

func TestCoreSkillsMirrorResources(t *testing.T) {
	resourceFS := resources.FS()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate core skill source")
	}
	sourceRoot := filepath.Join(filepath.Dir(thisFile), "skills")
	sourceFS := os.DirFS(sourceRoot)
	sourceFiles := make(map[string][]byte)
	err := fs.WalkDir(sourceFS, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sourceFS, name)
		if err != nil {
			return err
		}
		sourceFiles[name] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mirrorFiles := make(map[string][]byte)
	err = fs.WalkDir(resourceFS, "skills/core", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(resourceFS, name)
		if err != nil {
			return err
		}
		mirrorFiles[name[len("skills/core/"):]] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceFiles) != len(mirrorFiles) {
		t.Errorf("embedded core skill file count = %d, mirror file count = %d", len(sourceFiles), len(mirrorFiles))
	}
	for name, data := range sourceFiles {
		mirror, ok := mirrorFiles[name]
		if !ok {
			t.Errorf("core skill mirror is missing: %s", name)
			continue
		}
		if !bytes.Equal(data, mirror) {
			t.Errorf("core skill mirror differs: %s", name)
		}
	}
	for name := range mirrorFiles {
		if _, ok := sourceFiles[name]; !ok {
			t.Errorf("core skill mirror has stale file: %s", name)
		}
	}

	mirrorRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "resources", "skills", "core")
	sourceDisk := diskSkillFiles(t, sourceRoot)
	mirrorDisk := diskSkillFiles(t, mirrorRoot)
	if len(sourceDisk) != len(mirrorDisk) {
		t.Errorf("disk core skill file count = %d, mirror file count = %d", len(sourceDisk), len(mirrorDisk))
	}
	for name, want := range sourceDisk {
		got, ok := mirrorDisk[name]
		if !ok {
			t.Errorf("disk core skill mirror is missing: %s", name)
			continue
		}
		if !bytes.Equal(got.data, want.data) {
			t.Errorf("disk core skill mirror differs: %s", name)
		}
		if got.mode != want.mode {
			t.Errorf("core skill mirror mode %s = %s, want %s", name, got.mode, want.mode)
		}
	}
	for name := range mirrorDisk {
		if _, ok := sourceDisk[name]; !ok {
			t.Errorf("disk core skill mirror has stale file: %s", name)
		}
	}
}

type diskSkillFile struct {
	data []byte
	mode os.FileMode
}

func diskSkillFiles(t *testing.T, root string) map[string]diskSkillFile {
	t.Helper()
	files := make(map[string]diskSkillFile)
	diskFS := os.DirFS(root)
	err := fs.WalkDir(diskFS, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(diskFS, name)
		if err != nil {
			return err
		}
		files[name] = diskSkillFile{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatalf("walk skill tree %s: %v", root, err)
	}
	return files
}
