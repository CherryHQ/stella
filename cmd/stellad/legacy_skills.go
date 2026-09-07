package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/resources"
)

// legacySkillBlocker is an unsupported entry in the retired extracted builtin
// projection. Path is relative to $STELLA_HOME/.agents/skills using slash paths.
type legacySkillBlocker struct {
	Path string
	Kind string
}

// inventoryLegacySkills classifies the legacy extracted projection without
// changing it. Manifest file paths are inert old derived bytes and may have
// different contents or modes; every other entry blocks the bundle cutover.
func inventoryLegacySkills(skillsDir string, skills []resources.BuiltinSkillDescriptor) ([]legacySkillBlocker, error) {
	if skillsDir == "" {
		return nil, nil
	}
	info, err := os.Lstat(skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("legacy skills root %q must be a directory, not %s", skillsDir, legacyEntryType(info.Mode()))
	}

	expectedFiles, expectedDirs := legacyProjectionPaths(skills)
	var blockers []legacySkillBlocker
	if err := inventoryLegacyDir(skillsDir, ".", expectedFiles, expectedDirs, &blockers); err != nil {
		return nil, err
	}
	slices.SortFunc(blockers, func(a, b legacySkillBlocker) int {
		if order := strings.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	return blockers, nil
}

func legacyProjectionPaths(skills []resources.BuiltinSkillDescriptor) (map[string]struct{}, map[string]struct{}) {
	files := make(map[string]struct{})
	dirs := map[string]struct{}{".": {}}
	for _, skill := range skills {
		for _, file := range skill.Files {
			filePath := path.Join(skill.Root, file.Path)
			files[filePath] = struct{}{}
			for dir := path.Dir(filePath); dir != "."; dir = path.Dir(dir) {
				dirs[dir] = struct{}{}
			}
		}
	}
	return files, dirs
}

func inventoryLegacyDir(root, relative string, expectedFiles, expectedDirs map[string]struct{}, blockers *[]legacySkillBlocker) error {
	entries, err := os.ReadDir(legacyDiskPath(root, relative))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		relativePath := entry.Name()
		if relative != "." {
			relativePath = path.Join(relative, relativePath)
		}
		filename := legacyDiskPath(root, relativePath)
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		_, expectedFile := expectedFiles[relativePath]
		_, expectedDir := expectedDirs[relativePath]

		switch {
		case info.IsDir():
			if expectedFile {
				return legacyUnexpectedExpectedEntry(relativePath, info.Mode(), "regular file")
			}
			kind, err := legacyDirKind(filename)
			if err != nil {
				return err
			}
			if !expectedDir && kind == "skill_root" {
				*blockers = append(*blockers, legacySkillBlocker{Path: relativePath, Kind: "skill_root"})
				continue
			}
			before := len(*blockers)
			if err := inventoryLegacyDir(root, relativePath, expectedFiles, expectedDirs, blockers); err != nil {
				return err
			}
			if !expectedDir && len(*blockers) == before {
				*blockers = append(*blockers, legacySkillBlocker{Path: relativePath, Kind: kind})
			}
		case info.Mode()&fs.ModeSymlink != 0:
			if expectedFile || expectedDir {
				return legacyUnexpectedExpectedEntry(relativePath, info.Mode(), expectedLegacyType(expectedFile))
			}
			*blockers = append(*blockers, legacySkillBlocker{Path: relativePath, Kind: "residual_path"})
		case info.Mode().IsRegular():
			if expectedDir {
				return legacyUnexpectedExpectedEntry(relativePath, info.Mode(), "directory")
			}
			if !expectedFile {
				*blockers = append(*blockers, legacySkillBlocker{Path: relativePath, Kind: "residual_path"})
			}
		default:
			if expectedFile || expectedDir {
				return legacyUnexpectedExpectedEntry(relativePath, info.Mode(), expectedLegacyType(expectedFile))
			}
			*blockers = append(*blockers, legacySkillBlocker{Path: relativePath, Kind: "residual_path"})
		}
	}
	return nil
}

func legacyDirKind(dir string) (string, error) {
	info, err := os.Lstat(filepath.Join(dir, "SKILL.md"))
	if err == nil && info.Mode().IsRegular() {
		return "skill_root", nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return "residual_path", nil
}

func legacyDiskPath(root, relative string) string {
	if relative == "." {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func expectedLegacyType(file bool) string {
	if file {
		return "regular file"
	}
	return "directory"
}

func legacyUnexpectedExpectedEntry(relative string, mode fs.FileMode, expected string) error {
	return fmt.Errorf("legacy manifest path %q must be a %s, not %s", relative, expected, legacyEntryType(mode))
}

func legacyEntryType(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "regular file"
	default:
		return mode.Type().String()
	}
}
