package skills

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PathResolver maps a skill's (scope, agentID, userID) to the base directory
// where the skill's subdirectory lives on disk (e.g. "$STELLA_HOME/.agents/skills").
// Return empty string to skip disk mirroring for a given scope.
type PathResolver func(scope, agentID string, userID string) string

// DiskSyncStore wraps a Store and mirrors every write to disk so that skill
// scripts can be executed from the filesystem inside a sandbox.
//
// Write ordering is disk-first: if the disk write fails the DB write is skipped
// and the error is propagated. On crash after disk write but before DB write,
// MigrateFilesystem at next startup re-imports the orphaned disk files.
//
// For Delete, DB is removed first (disk after) to avoid orphaned skill
// directories re-importing on next startup as if they were new skills.
type DiskSyncStore struct {
	Store
	resolver PathResolver
}

// NewDiskSyncStore wraps inner with disk mirroring using the given resolver.
func NewDiskSyncStore(inner Store, resolver PathResolver) *DiskSyncStore {
	return &DiskSyncStore{Store: inner, resolver: resolver}
}

func (d *DiskSyncStore) Create(ctx context.Context, sk Skill, files map[string]string) (string, error) {
	base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
	if base != "" {
		if err := d.writeFilesToDisk(base, sk.Name, files); err != nil {
			return "", fmt.Errorf("disk_sync create %q: %w", sk.Name, err)
		}
	}
	return d.Store.Create(ctx, sk, files)
}

func (d *DiskSyncStore) UpsertFile(ctx context.Context, skillID, path, content string) error {
	sk, err := d.findByID(ctx, skillID)
	if err != nil {
		return err
	}
	if sk != nil {
		base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
		if base != "" {
			diskPath, err := safeDiskPath(base, sk.Name, filepath.FromSlash(path))
			if err != nil {
				return fmt.Errorf("disk_sync upsert file %q in skill %q: %w", path, sk.Name, err)
			}
			if err := writeFile(diskPath, content); err != nil {
				return fmt.Errorf("disk_sync upsert file %q in skill %q: %w", path, sk.Name, err)
			}
		}
	}
	return d.Store.UpsertFile(ctx, skillID, path, content)
}

func (d *DiskSyncStore) DeleteFile(ctx context.Context, skillID, path string) error {
	sk, err := d.findByID(ctx, skillID)
	if err != nil {
		return err
	}
	if sk != nil {
		base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
		if base != "" {
			diskPath, err := safeDiskPath(base, sk.Name, filepath.FromSlash(path))
			if err != nil {
				return fmt.Errorf("disk_sync delete file %q in skill %q: %w", path, sk.Name, err)
			}
			if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("disk_sync delete file %q in skill %q: %w", path, sk.Name, err)
			}
		}
	}
	return d.Store.DeleteFile(ctx, skillID, path)
}

// Delete removes the DB row first, then removes the disk directory.
// DB-first ordering prevents MigrateFilesystem from re-importing orphaned dirs.
func (d *DiskSyncStore) Delete(ctx context.Context, id string, vc ViewContext) error {
	sk, err := d.findByID(ctx, id)
	if err != nil {
		return err
	}
	if err := d.Store.Delete(ctx, id, vc); err != nil {
		return err
	}
	d.removeSkillDir(ctx, sk)
	return nil
}

func (d *DiskSyncStore) UpdateSystemSkill(ctx context.Context, id string, patch UpdatePatch) error {
	store, ok := d.Store.(interface {
		UpdateSystemSkill(context.Context, string, UpdatePatch) error
	})
	if !ok {
		return fmt.Errorf("disk_sync: inner store does not support system skill update")
	}
	return store.UpdateSystemSkill(ctx, id, patch)
}

func (d *DiskSyncStore) DeleteSystemSkill(ctx context.Context, id string) error {
	sk, err := d.findByID(ctx, id)
	if err != nil {
		return err
	}
	store, ok := d.Store.(interface {
		DeleteSystemSkill(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("disk_sync: inner store does not support system skill delete")
	}
	if err := store.DeleteSystemSkill(ctx, id); err != nil {
		return err
	}
	d.removeSkillDir(ctx, sk)
	return nil
}

func (d *DiskSyncStore) removeSkillDir(ctx context.Context, sk *Skill) {
	if sk == nil {
		return
	}
	base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
	if base == "" {
		return
	}
	skillDir, pathErr := safeDiskPath(base, sk.Name)
	if pathErr != nil {
		return
	}
	if err := os.RemoveAll(skillDir); err != nil {
		slog.WarnContext(ctx, "disk_sync: failed to remove skill dir after DB delete",
			"skill", sk.Name, "dir", skillDir, "err", err)
	}
}

// SyncAllToDisk materializes every skill in the DB to disk. The DB is the
// single source of truth: files absent on disk are created, stale files are
// overwritten, and disk files not present in the DB are removed.
func (d *DiskSyncStore) SyncAllToDisk(ctx context.Context) error {
	all, err := d.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("disk_sync sync_all: list: %w", err)
	}
	for _, sk := range all {
		base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
		if base == "" {
			continue
		}
		skillDir, pathErr := safeDiskPath(base, sk.Name)
		if pathErr != nil {
			slog.WarnContext(ctx, "disk_sync sync_all: skipping skill with unsafe name", "name", sk.Name, "err", pathErr)
			continue
		}
		files, err := d.ListFilesWithContent(ctx, sk.ID)
		if err != nil {
			return fmt.Errorf("disk_sync sync_all: list files for %q: %w", sk.Name, err)
		}
		for p, content := range files {
			diskPath, pathErr := safeDiskPath(skillDir, filepath.FromSlash(p))
			if pathErr != nil {
				slog.WarnContext(ctx, "disk_sync sync_all: skipping file with unsafe path", "skill", sk.Name, "path", p, "err", pathErr)
				continue
			}
			if existing, readErr := os.ReadFile(diskPath); readErr == nil && bytes.Equal(existing, []byte(content)) {
				continue
			}
			if err := writeFile(diskPath, content); err != nil {
				return fmt.Errorf("disk_sync sync_all: write %q in %q: %w", p, sk.Name, err)
			}
		}
		removeOrphanDiskFiles(ctx, skillDir, files, sk.Name)
	}
	return nil
}

// removeOrphanDiskFiles deletes files under skillDir that are not in dbFiles.
func removeOrphanDiskFiles(ctx context.Context, skillDir string, dbFiles map[string]string, skillName string) {
	_ = filepath.WalkDir(skillDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, relErr := filepath.Rel(skillDir, p)
		if relErr != nil {
			return relErr
		}
		if _, inDB := dbFiles[filepath.ToSlash(rel)]; !inDB {
			if rmErr := os.Remove(p); rmErr == nil {
				slog.InfoContext(ctx, "disk_sync sync_all: removed orphan file", "skill", skillName, "path", rel)
			}
		}
		return nil
	})
}

// ListKnowledge forwards to the inner store if it implements KnowledgeStore.
// DiskSyncStore embeds Store (not KnowledgeStore), so without this method,
// skillStoreAdapter's type assertion to KnowledgeStore would fail silently.
func (d *DiskSyncStore) ListKnowledge(ctx context.Context, vc ViewContext, types ...KnowledgeType) ([]KnowledgeEntry, error) {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.ListKnowledge(ctx, vc, types...)
	}
	return nil, nil
}

// CreateKnowledge forwards first-class knowledge writes to the inner store.
func (d *DiskSyncStore) CreateKnowledge(ctx context.Context, params KnowledgeCreateParams) (KnowledgeEntry, error) {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.CreateKnowledge(ctx, params)
	}
	return KnowledgeEntry{}, fmt.Errorf("disk_sync: inner store does not support knowledge create")
}

func (d *DiskSyncStore) ListKnowledgeByNameAndScope(ctx context.Context, name string, scope string, userID string, agentID string) ([]KnowledgeEntry, error) {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.ListKnowledgeByNameAndScope(ctx, name, scope, userID, agentID)
	}
	return nil, nil
}

func (d *DiskSyncStore) UpdateKnowledge(ctx context.Context, params KnowledgeUpdateParams) (KnowledgeEntry, error) {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.UpdateKnowledge(ctx, params)
	}
	return KnowledgeEntry{}, fmt.Errorf("disk_sync: inner store does not support knowledge update")
}

func (d *DiskSyncStore) DeprecateKnowledge(ctx context.Context, id string) error {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.DeprecateKnowledge(ctx, id)
	}
	return fmt.Errorf("disk_sync: inner store does not support knowledge deprecate")
}

// ExpireKnowledgeDraftsByType forwards to the inner store if it implements KnowledgeStore.
func (d *DiskSyncStore) ExpireKnowledgeDraftsByType(ctx context.Context, knowledgeType KnowledgeType, before time.Time) error {
	if ks, ok := d.Store.(KnowledgeStore); ok {
		return ks.ExpireKnowledgeDraftsByType(ctx, knowledgeType, before)
	}
	return nil
}

// findByID looks up a skill by ID using ListAll. This is only called on write
// paths which are not hot.
func (d *DiskSyncStore) findByID(ctx context.Context, id string) (*Skill, error) {
	all, err := d.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("disk_sync: look up skill %q: %w", id, err)
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (d *DiskSyncStore) writeFilesToDisk(base, skillName string, files map[string]string) error {
	skillDir, err := safeDiskPath(base, skillName)
	if err != nil {
		return err
	}
	for p, content := range files {
		diskPath, err := safeDiskPath(skillDir, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		if err := writeFile(diskPath, content); err != nil {
			return err
		}
	}
	return nil
}

// safeDiskPath resolves a file path under base and ensures it doesn't escape
// via directory traversal (e.g. "../" in skill name or file path).
func safeDiskPath(base string, parts ...string) (string, error) {
	joined := filepath.Join(append([]string{base}, parts...)...)
	cleaned := filepath.Clean(joined)
	if !strings.HasPrefix(cleaned, filepath.Clean(base)+string(filepath.Separator)) && cleaned != filepath.Clean(base) {
		return "", fmt.Errorf("path %q escapes base %q", cleaned, base)
	}
	return cleaned, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Compile-time assertions.
var (
	_ Store          = (*DiskSyncStore)(nil)
	_ KnowledgeStore = (*DiskSyncStore)(nil)
)
