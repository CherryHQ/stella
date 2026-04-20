package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

var (
	ensureSkillsMu    sync.Mutex
	ensureSkillsState = map[string]*ensureOnce{}
)

type ensureOnce struct {
	once sync.Once
	err  error
}

// EnsureBuiltinSkills extracts builtin skills once per (process, skillsDir) for
// callers that need a lazy, idempotent fallback outside normal app startup.
func EnsureBuiltinSkills(skillsDir string) error {
	if skillsDir == "" {
		return nil
	}
	skillsDir = filepath.Clean(skillsDir)
	ensureSkillsMu.Lock()
	state, ok := ensureSkillsState[skillsDir]
	if !ok {
		state = &ensureOnce{}
		ensureSkillsState[skillsDir] = state
	}
	ensureSkillsMu.Unlock()
	state.once.Do(func() { state.err = ExtractSkills(skillsDir) })
	return state.err
}

// ExtractSkills writes every builtin skill directory into skillsDir.
// Each skill's subdirectory is replaced atomically; other entries in skillsDir
// are preserved.
func ExtractSkills(skillsDir string) error {
	sub, ok := SubFS(KindSkill)
	if !ok {
		return nil
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return fmt.Errorf("read builtin skills: %w", err)
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := os.RemoveAll(filepath.Join(skillsDir, name)); err != nil {
			return fmt.Errorf("clean builtin skill %q: %w", name, err)
		}
		if err := copySubtree(sub, name, filepath.Join(skillsDir, name)); err != nil {
			return fmt.Errorf("extract builtin skill %q: %w", name, err)
		}
	}
	return nil
}

// ExtractSubAgents writes builtin sub-agent preset files into agentsDir.
// Individual files are overwritten; other content in agentsDir is preserved.
func ExtractSubAgents(agentsDir string) error {
	sub, ok := SubFS(KindSubAgent)
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
