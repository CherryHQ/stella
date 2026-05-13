package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// MigrateFSConfig controls how on-disk skills are discovered and mapped to DB rows.
type MigrateFSConfig struct {
	// StellaHome is intentionally unused: builtin (stella source) skills are handled
	// exclusively by SyncBuiltin, not by filesystem migration.
	StellaHome    string
	AgentRoot     string
	UserRoot      string
	UserSkillsDir string
	// Owner fields used when creating scoped rows.
	UserID  int64
	AgentID string
}

// MigrateFSResult contains counts for a MigrateFilesystem run.
type MigrateFSResult struct {
	Imported int
	Skipped  int // already-in-DB duplicates
}

// migrateLoadedSkill holds the skill data discovered from the filesystem.
type migrateLoadedSkill struct {
	Name                   string
	Description            string
	Status                 string
	CreatedAt              string
	DisableModelInvocation bool
	BaseDir                string
	Source                 string // agent | user | project
}

// MigrateFilesystem reads on-disk skills and idempotently imports each into the DB.
//
// Skills with source="stella" (builtin) are skipped — use SyncBuiltin for those.
// The function is safe to call multiple times; existing rows are counted as Skipped.
func MigrateFilesystem(ctx context.Context, store Store, cfg MigrateFSConfig) (MigrateFSResult, error) {
	loaded := loadFSSkills(cfg)

	allSkills, err := store.ListAll(ctx)
	if err != nil {
		return MigrateFSResult{}, fmt.Errorf("migrate_fs: list all skills: %w", err)
	}

	var result MigrateFSResult

	for _, cs := range loaded {
		if cs.Source == "stella" {
			// Builtin skills are managed by SyncBuiltin, not filesystem migration.
			continue
		}

		scope, err := sourceToScope(cs.Source)
		if err != nil {
			return result, fmt.Errorf("migrate_fs: skill %q: %w", cs.Name, err)
		}

		// Check for existing row — skip if already imported.
		exists, checkErr := skillExistsInDB(allSkills, scope, cs.Name, cfg)
		if checkErr != nil {
			return result, fmt.Errorf("migrate_fs: check existing %q: %w", cs.Name, checkErr)
		}
		if exists {
			result.Skipped++
			continue
		}

		// Walk the skill directory and collect all files.
		files, err := walkSkillDir(cs.BaseDir)
		if err != nil {
			return result, fmt.Errorf("migrate_fs: walk %q: %w", cs.BaseDir, err)
		}

		// Build metadata JSON from catalog skill fields.
		meta, err := json.Marshal(map[string]any{
			"disable-model-invocation": cs.DisableModelInvocation,
			"created-at":               cs.CreatedAt,
		})
		if err != nil {
			return result, fmt.Errorf("migrate_fs: marshal metadata %q: %w", cs.Name, err)
		}

		sk := Skill{
			Scope:                  scope,
			UserID:                 cfg.UserID,
			AgentID:                cfg.AgentID,
			Name:                   cs.Name,
			Description:            cs.Description,
			Status:                 cs.Status,
			DisableModelInvocation: cs.DisableModelInvocation,
			Metadata:               json.RawMessage(meta),
		}

		if _, err := store.Create(ctx, sk, files); err != nil {
			return result, fmt.Errorf("migrate_fs: create %q: %w", cs.Name, err)
		}
		result.Imported++
	}

	return result, nil
}

// loadFSSkills discovers skills from the filesystem in increasing priority order:
// agent -> user. Builtin (stella source) and project skills are intentionally excluded here.
// Project skills are filesystem-only and do not get migrated to the DB.
func loadFSSkills(cfg MigrateFSConfig) []migrateLoadedSkill {
	type key struct{ source, name string }
	indexByKey := map[key]int{}
	var loaded []migrateLoadedSkill

	add := func(s migrateLoadedSkill) {
		k := key{source: s.Source, name: s.Name}
		if idx, ok := indexByKey[k]; ok {
			loaded[idx] = s
			return
		}
		indexByKey[k] = len(loaded)
		loaded = append(loaded, s)
	}

	dedupPaths := map[string]bool{}
	addDir := func(dir, source string) {
		abs, _ := filepath.Abs(dir)
		if dedupPaths[abs] {
			return
		}
		dedupPaths[abs] = true
		for _, s := range scanFSSkillDir(dir, source) {
			add(s)
		}
	}

	if cfg.AgentRoot != "" {
		addDir(filepath.Join(cfg.AgentRoot, ".agents", "skills"), "agent")
	}

	userSkillsDir := cfg.UserSkillsDir
	if userSkillsDir == "" && cfg.UserRoot != "" {
		userSkillsDir = filepath.Join(cfg.UserRoot, ".agents", "skills")
	}
	if userSkillsDir != "" {
		addDir(userSkillsDir, "user")
	}

	return loaded
}

// scanFSSkillDir lists top-level skill directories under dir and parses each SKILL.md.
func scanFSSkillDir(dir, source string) []migrateLoadedSkill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("migrate_fs: cannot read skills dir", "dir", dir, "err", err)
		}
		return nil
	}

	var skills []migrateLoadedSkill
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(dir, name)
		skillMD := filepath.Join(skillDir, "SKILL.md")

		data, err := os.ReadFile(skillMD)
		if err != nil {
			continue // no SKILL.md in this dir
		}

		fm, err := parseBuiltinFrontmatter(string(data))
		if err != nil {
			slog.Warn("migrate_fs: cannot parse frontmatter", "path", skillMD, "err", err)
			continue
		}
		if strings.TrimSpace(fm.Description) == "" {
			continue
		}
		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			continue
		}
		// Validate name matches directory (mirrors the old loadSkillFromFile check).
		if skillName != name {
			slog.Warn("migrate_fs: skill name does not match directory, skipping",
				"name", skillName, "dir", name)
			continue
		}

		skills = append(skills, migrateLoadedSkill{
			Name:                   skillName,
			Description:            fm.Description,
			Status:                 normalizeBuiltinStatus(fm.Status),
			CreatedAt:              fm.CreatedAt,
			DisableModelInvocation: fm.DisableModelInvocation,
			BaseDir:                skillDir,
			Source:                 source,
		})
	}
	return skills
}

// sourceToScope maps catalog source labels to DB scope values.
func sourceToScope(source string) (string, error) {
	switch source {
	case "agent":
		return "agent", nil
	case "user":
		return "user", nil
	default:
		return "", fmt.Errorf("unknown source %q", source)
	}
}

// skillExistsInDB checks whether a skill with the given (scope, name, owner) is already
// present in the pre-fetched skill list.
func skillExistsInDB(all []Skill, scope, name string, cfg MigrateFSConfig) (bool, error) {
	for _, sk := range all {
		if sk.Scope != scope || sk.Name != name {
			continue
		}
		switch scope {
		case "agent":
			if sk.AgentID == cfg.AgentID {
				return true, nil
			}
		case "user":
			if sk.UserID == cfg.UserID {
				return true, nil
			}
		}
	}
	return false, nil
}

// walkSkillDir recursively collects all files under baseDir into a map keyed by
// path relative to baseDir (e.g. "SKILL.md", "references/api.md").
func walkSkillDir(baseDir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	return files, err
}
