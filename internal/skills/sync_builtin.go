package skills

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"
)

// builtinFrontmatter mirrors the YAML frontmatter fields in a builtin SKILL.md.
// Duplicated from plugins/tools/skills/catalog.go intentionally (Phase 6 will decide
// whether to centralise; for now this avoids breaking catalog.go's unexported API).
type builtinFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	Status                 string `yaml:"status"`
	CreatedAt              string `yaml:"created-at"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

// parseBuiltinFrontmatter extracts YAML frontmatter from a SKILL.md string.
// It mirrors parseFrontmatter in plugins/tools/skills/catalog.go.
func parseBuiltinFrontmatter(content string) (builtinFrontmatter, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return builtinFrontmatter{}, fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return builtinFrontmatter{}, fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx]

	var fm builtinFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return builtinFrontmatter{}, fmt.Errorf("invalid yaml: %w", err)
	}
	return fm, nil
}

// frontmatterToMetaJSON marshals a subset of frontmatter fields into a JSON blob
// suitable for Skill.Metadata storage.
func frontmatterToMetaJSON(fm builtinFrontmatter) (json.RawMessage, error) {
	m := map[string]any{
		"created-at":               fm.CreatedAt,
		"disable-model-invocation": fm.DisableModelInvocation,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// hashContent returns a hex SHA-256 digest of s.
func hashContent(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// normalizeBuiltinStatus applies the same defaulting as NormalizeSkillStatus in catalog.go.
func normalizeBuiltinStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "draft":
		return "draft"
	case "deprecated":
		return "deprecated"
	default:
		return "active"
	}
}

// SyncBuiltin imports or updates builtin skills from the embedded FS into the DB.
// Only scope='system' rows are created/updated; user/agent/project rows are never touched.
//
// Called at server start (wiring happens in Phase 4; this function is exported for that use).
//
// TODO(orphan-cleanup): Skills that exist in the DB as scope='system' but are absent from
// builtinFS are not deleted here. Phase 4+ should add an explicit tombstone pass once the
// full lifecycle is understood.
func SyncBuiltin(ctx context.Context, store *SQLiteStore, builtinFS fs.FS) error {
	top, err := fs.ReadDir(builtinFS, ".")
	if err != nil {
		return fmt.Errorf("sync_builtin: read root: %w", err)
	}

	for _, entry := range top {
		if !entry.IsDir() {
			continue
		}
		skillName := entry.Name()

		// Only process directories that contain a SKILL.md at their top level.
		skillMDPath := skillName + "/" + MainFile
		if _, err := fs.Stat(builtinFS, skillMDPath); err != nil {
			slog.DebugContext(ctx, "sync_builtin: skipping dir without SKILL.md", "dir", skillName)
			continue
		}

		if err := syncBuiltinSkill(ctx, store, builtinFS, skillName); err != nil {
			slog.ErrorContext(ctx, "sync_builtin: failed to sync skill", "name", skillName, "err", err)
			return fmt.Errorf("sync_builtin: skill %q: %w", skillName, err)
		}
	}
	return nil
}

// syncBuiltinSkill upserts a single builtin skill directory into the DB.
func syncBuiltinSkill(ctx context.Context, store *SQLiteStore, builtinFS fs.FS, skillName string) error {
	// 1. Read and parse SKILL.md.
	skillMDPath := skillName + "/" + MainFile
	mainRaw, err := fs.ReadFile(builtinFS, skillMDPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", skillMDPath, err)
	}
	mainContent := string(mainRaw)

	fm, err := parseBuiltinFrontmatter(mainContent)
	if err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return fmt.Errorf("frontmatter name is empty")
	}
	if fm.Name != skillName {
		slog.WarnContext(ctx, "sync_builtin: name mismatch, skipping",
			"frontmatter_name", fm.Name, "dir_name", skillName)
		return nil
	}

	// 2. Collect all files under the skill directory, keyed by path relative to skillName/.
	files := map[string]string{}
	if err := fs.WalkDir(builtinFS, skillName, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(builtinFS, path)
		if err != nil {
			return err
		}
		// Strip the "skillName/" prefix so paths are relative to the skill root.
		relPath := strings.TrimPrefix(path, skillName+"/")
		files[relPath] = string(raw)
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", skillName, err)
	}

	// 3. Build metadata JSON.
	meta, err := frontmatterToMetaJSON(fm)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	// 4. Check for existing system skill row.
	existing, err := store.q.GetSystemSkillByName(ctx, fm.Name)
	if errors.Is(err, sql.ErrNoRows) {
		// New skill — insert.
		sk := Skill{
			Scope:                  "system",
			Name:                   fm.Name,
			Description:            fm.Description,
			Status:                 normalizeBuiltinStatus(fm.Status),
			DisableModelInvocation: fm.DisableModelInvocation,
			Metadata:               meta,
		}
		if _, err := store.Create(ctx, sk, files); err != nil {
			return fmt.Errorf("create: %w", err)
		}
		slog.InfoContext(ctx, "sync_builtin: created skill", "name", fm.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("query existing: %w", err)
	}

	skillID := existing.ID

	// 5. Update metadata if frontmatter changed.
	disabledInt := int64(0)
	if fm.DisableModelInvocation {
		disabledInt = 1
	}
	if existing.Description != fm.Description ||
		existing.Status != normalizeBuiltinStatus(fm.Status) ||
		existing.DisableModelInvocation != disabledInt ||
		existing.Metadata != string(meta) {
		if err := store.Update(ctx, skillID, UpdatePatch{
			Description:            &fm.Description,
			Status:                 strPtr(normalizeBuiltinStatus(fm.Status)),
			DisableModelInvocation: &fm.DisableModelInvocation,
			Metadata:               meta,
		}); err != nil {
			return fmt.Errorf("update metadata: %w", err)
		}
	}

	// 6. Sync file contents — upsert changed files.
	existingFiles, err := store.q.ListSkillFiles(ctx, skillID)
	if err != nil {
		return fmt.Errorf("list existing files: %w", err)
	}
	existingByPath := map[string]string{}
	for _, f := range existingFiles {
		existingByPath[f.Path] = f.Content
	}

	for path, content := range files {
		if existing, ok := existingByPath[path]; ok && hashContent(existing) == hashContent(content) {
			continue // unchanged
		}
		if err := store.UpsertFile(ctx, skillID, path, content); err != nil {
			return fmt.Errorf("upsert file %q: %w", path, err)
		}
	}

	// 7. Delete obsolete files (present in DB but removed from embedded FS).
	for path := range existingByPath {
		if _, stillPresent := files[path]; !stillPresent {
			if err := store.DeleteFile(ctx, skillID, path); err != nil {
				return fmt.Errorf("delete obsolete file %q: %w", path, err)
			}
			slog.InfoContext(ctx, "sync_builtin: deleted obsolete file", "skill", fm.Name, "path", path)
		}
	}

	slog.DebugContext(ctx, "sync_builtin: synced skill", "name", fm.Name, "id", skillID)
	return nil
}

// strPtr is a tiny helper that returns a pointer to s.
func strPtr(s string) *string { return &s }
