package skills

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// builtinFrontmatter mirrors the YAML frontmatter fields in a builtin SKILL.md.
// Duplicated from internal/tools/skills/catalog.go intentionally (Phase 6 will decide
// whether to centralise; for now this avoids breaking catalog.go's unexported API).
type builtinFrontmatter struct {
	Name                   string         `yaml:"name"`
	Description            string         `yaml:"description"`
	Status                 string         `yaml:"status"`
	CreatedAt              string         `yaml:"created-at"`
	DisableModelInvocation bool           `yaml:"disable-model-invocation"`
	Metadata               map[string]any `yaml:"metadata"`
}

// parseBuiltinFrontmatter extracts YAML frontmatter from a SKILL.md string.
// It mirrors parseFrontmatter in internal/tools/skills/catalog.go.
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
	m := map[string]any{}
	maps.Copy(m, fm.Metadata)
	if fm.CreatedAt != "" {
		m["created-at"] = fm.CreatedAt
	}
	if fm.DisableModelInvocation {
		m["disable-model-invocation"] = fm.DisableModelInvocation
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
func SyncBuiltin(ctx context.Context, store Store, builtinFS fs.FS) error {
	var skillRoots []string
	err := fs.WalkDir(builtinFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == "." {
			return nil
		}
		if _, err := fs.Stat(builtinFS, path.Join(p, MainFile)); err == nil {
			skillRoots = append(skillRoots, p)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync_builtin: read root: %w", err)
	}

	allSkills, err := store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("sync_builtin: list all skills: %w", err)
	}

	desired := make(map[string]struct{}, len(skillRoots))
	for _, skillRoot := range skillRoots {
		desired[path.Base(skillRoot)] = struct{}{}
		if err := syncBuiltinSkill(ctx, store, builtinFS, skillRoot, allSkills); err != nil {
			slog.ErrorContext(ctx, "sync_builtin: failed to sync skill", "path", skillRoot, "err", err)
			return fmt.Errorf("sync_builtin: skill %q: %w", skillRoot, err)
		}
	}
	if err := deleteRemovedSystemSkills(ctx, store, allSkills, desired); err != nil {
		return fmt.Errorf("sync_builtin: delete removed system skills: %w", err)
	}
	return nil
}

func deleteRemovedSystemSkills(ctx context.Context, store Store, rows []Skill, desired map[string]struct{}) error {
	for _, row := range rows {
		if row.Scope != "system" {
			continue
		}
		if _, ok := desired[row.Name]; ok {
			continue
		}
		if err := store.Delete(ctx, row.ID, ViewContext{}); err != nil {
			return fmt.Errorf("delete skill %q (%s): %w", row.Name, row.ID, err)
		}
		slog.InfoContext(ctx, "sync_builtin: deleted removed system skill", "name", row.Name, "id", row.ID)
	}
	return nil
}

// syncBuiltinSkill upserts a single builtin skill directory into the DB.
func syncBuiltinSkill(ctx context.Context, store Store, builtinFS fs.FS, skillRoot string, allSkills []Skill) error {
	skillName := path.Base(skillRoot)
	// 1. Read and parse SKILL.md.
	skillMDPath := path.Join(skillRoot, MainFile)
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
	if err := fs.WalkDir(builtinFS, skillRoot, func(path string, d fs.DirEntry, err error) error {
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
		// Strip the skill root prefix so paths are relative to the skill root.
		relPath := strings.TrimPrefix(path, skillRoot+"/")
		files[relPath] = string(raw)
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", skillRoot, err)
	}

	// 3. Build metadata JSON.
	meta, err := frontmatterToMetaJSON(fm)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	// 4. Check for existing system skill row.
	var existing *Skill
	for i := range allSkills {
		if allSkills[i].Scope == "system" && allSkills[i].Name == fm.Name {
			existing = &allSkills[i]
			break
		}
	}
	if existing == nil {
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

	skillID := existing.ID

	// 5. Update metadata if frontmatter changed.
	if existing.Description != fm.Description ||
		existing.Status != normalizeBuiltinStatus(fm.Status) ||
		existing.DisableModelInvocation != fm.DisableModelInvocation ||
		string(existing.Metadata) != string(meta) {
		if err := store.Update(ctx, skillID, ViewContext{}, UpdatePatch{
			Description:            &fm.Description,
			Status:                 strPtr(normalizeBuiltinStatus(fm.Status)),
			DisableModelInvocation: &fm.DisableModelInvocation,
			Metadata:               meta,
		}); err != nil {
			return fmt.Errorf("update metadata: %w", err)
		}
	}

	// 6. Sync file contents — upsert changed files.
	existingByPath, err := store.ListFilesWithContent(ctx, skillID)
	if err != nil {
		return fmt.Errorf("list existing files: %w", err)
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
