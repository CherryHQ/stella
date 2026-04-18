package skills

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/pkg/db/sqlc"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	catalogskills "github.com/vaayne/anna/plugins/tools/skills"
)

// MigrateFSConfig controls how on-disk skills are discovered and mapped to DB rows.
type MigrateFSConfig struct {
	Runtime pkgplugins.ToolRuntime
	// AnnaHome is intentionally unused: builtin (anna source) skills are handled
	// exclusively by SyncBuiltin, not by filesystem migration.
	AnnaHome      string
	AgentRoot     string
	UserRoot      string
	ProjectRoot   string
	UserSkillsDir string
	// Owner fields used when creating scoped rows.
	UserID  int64
	AgentID string
	Project string // defaults to ProjectRoot if empty
}

// MigrateFSResult contains counts for a MigrateFilesystem run.
type MigrateFSResult struct {
	Imported int
	Skipped  int // already-in-DB duplicates
}

// MigrateFilesystem reads on-disk skills using the existing catalog loader and
// idempotently imports each into the DB.
//
// Skills with source="anna" (builtin) are skipped — use SyncBuiltin for those.
// The function is safe to call multiple times; existing rows are counted as Skipped.
func MigrateFilesystem(ctx context.Context, store *SQLiteStore, cfg MigrateFSConfig) (MigrateFSResult, error) {
	loaded := catalogskills.LoadSkills(ctx, catalogskills.LoadSkillsConfig{
		Runtime:       cfg.Runtime,
		AnnaHome:      "", // intentionally empty: skip builtin source
		AgentRoot:     cfg.AgentRoot,
		UserRoot:      cfg.UserRoot,
		ProjectRoot:   cfg.ProjectRoot,
		UserSkillsDir: cfg.UserSkillsDir,
	})

	var result MigrateFSResult

	for _, cs := range loaded {
		if cs.Source == "anna" {
			// Builtin skills are managed by SyncBuiltin, not filesystem migration.
			continue
		}

		scope, err := sourceToScope(cs.Source)
		if err != nil {
			return result, fmt.Errorf("migrate_fs: skill %q: %w", cs.Name, err)
		}

		// Check for existing row — skip if already imported.
		exists, checkErr := skillExistsInDB(ctx, store.q, scope, cs.Name, cfg)
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

		project := cfg.Project
		if project == "" && scope == "project" {
			project = cfg.ProjectRoot
		}

		sk := Skill{
			Scope:                  scope,
			UserID:                 cfg.UserID,
			AgentID:                cfg.AgentID,
			Project:                project,
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

// sourceToScope maps catalog source labels to DB scope values.
func sourceToScope(source string) (string, error) {
	switch source {
	case "agent":
		return "agent", nil
	case "user":
		return "user", nil
	case "project":
		return "project", nil
	default:
		return "", fmt.Errorf("unknown source %q", source)
	}
}

// skillExistsInDB checks whether a skill with the given (scope, name, owner) is already
// present in the DB. It dispatches to the appropriate typed query to avoid NULL = NULL pitfalls.
func skillExistsInDB(ctx context.Context, q *sqlc.Queries, scope, name string, cfg MigrateFSConfig) (bool, error) {
	var err error
	switch scope {
	case "agent":
		_, err = q.GetAgentSkillByName(ctx, sqlc.GetAgentSkillByNameParams{
			AgentID: sql.NullString{String: cfg.AgentID, Valid: cfg.AgentID != ""},
			Name:    name,
		})
	case "user":
		_, err = q.GetUserSkillByName(ctx, sqlc.GetUserSkillByNameParams{
			UserID: sql.NullInt64{Int64: cfg.UserID, Valid: cfg.UserID != 0},
			Name:   name,
		})
	case "project":
		project := cfg.Project
		if project == "" {
			project = cfg.ProjectRoot
		}
		_, err = q.GetProjectSkillByName(ctx, sqlc.GetProjectSkillByNameParams{
			Project: sql.NullString{String: project, Valid: project != ""},
			Name:    name,
		})
	default:
		return false, fmt.Errorf("unsupported scope %q", scope)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
