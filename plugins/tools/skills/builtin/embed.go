// Package builtin is the legacy skill-only shim for the unified builtin registry.
// All content moved to github.com/vaayne/anna/plugins/tools/builtin; this file
// keeps the pre-migration API working while callers migrate.
package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	newbuiltin "github.com/vaayne/anna/plugins/tools/builtin"
)

// BuiltinSkillFS returns the embedded filesystem containing builtin skills.
// Top-level entries are skill directories (each with a SKILL.md) — the shape
// SyncBuiltin expects.
func BuiltinSkillFS() fs.FS {
	sub, ok := newbuiltin.SubFS(newbuiltin.KindSkill)
	if !ok {
		return nil
	}
	return sub
}

var (
	builtinSkillsStateMu sync.Mutex
	builtinSkillsState   = map[string]*ensureState{}
)

type ensureState struct {
	once sync.Once
	err  error
}

func getBuiltinSkillsState(skillsDir string) *ensureState {
	skillsDir = filepath.Clean(skillsDir)
	builtinSkillsStateMu.Lock()
	defer builtinSkillsStateMu.Unlock()
	state := builtinSkillsState[skillsDir]
	if state == nil {
		state = &ensureState{}
		builtinSkillsState[skillsDir] = state
	}
	return state
}

// EnsureBuiltinSkills extracts builtin skills once per process for callers that
// need a lazy, idempotent fallback outside normal app startup.
func EnsureBuiltinSkills(skillsDir string) error {
	if skillsDir == "" {
		return nil
	}
	state := getBuiltinSkillsState(skillsDir)
	state.once.Do(func() {
		state.err = ExtractSkills(skillsDir)
	})
	return state.err
}

// ExtractSkills writes every builtin skill directory into skillsDir.
// Each skill's subdirectory is replaced atomically; other content in skillsDir is preserved.
func ExtractSkills(skillsDir string) error {
	skillsFS := BuiltinSkillFS()
	if skillsFS == nil {
		return nil
	}
	entries, err := fs.ReadDir(skillsFS, ".")
	if err != nil {
		return fmt.Errorf("read builtin skills: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := os.RemoveAll(filepath.Join(skillsDir, name)); err != nil {
			return fmt.Errorf("clean builtin skill %q: %w", name, err)
		}
		if err := copySubtree(skillsFS, name, filepath.Join(skillsDir, name)); err != nil {
			return fmt.Errorf("extract builtin skill %q: %w", name, err)
		}
	}
	return nil
}

// ExtractAgents writes builtin sub-agent preset files into agentsDir.
// Individual files are overwritten; other content in agentsDir is preserved.
func ExtractAgents(agentsDir string) error {
	sub, ok := newbuiltin.SubFS(newbuiltin.KindSubAgent)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return fmt.Errorf("read builtin subagents: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(sub, entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, entry.Name()), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// copySubtree copies every file under root within srcFS to dst on disk.
func copySubtree(srcFS fs.FS, root, dst string) error {
	return fs.WalkDir(srcFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(srcFS, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
