package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PathResolver maps a skill's (scope, agentID, userID) to the base directory
// where the skill's subdirectory lives on disk (e.g. "$STELLA_HOME/.agents/skills").
// Return empty string to skip disk mirroring for a given scope.
type PathResolver func(scope, agentID string, userID int64) string

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
			diskPath := filepath.Join(base, sk.Name, filepath.FromSlash(path))
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
			diskPath := filepath.Join(base, sk.Name, filepath.FromSlash(path))
			if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("disk_sync delete file %q in skill %q: %w", path, sk.Name, err)
			}
		}
	}
	return d.Store.DeleteFile(ctx, skillID, path)
}

// Delete removes the DB row first, then removes the disk directory.
// DB-first ordering prevents MigrateFilesystem from re-importing orphaned dirs.
func (d *DiskSyncStore) Delete(ctx context.Context, id string) error {
	sk, err := d.findByID(ctx, id)
	if err != nil {
		return err
	}
	if err := d.Store.Delete(ctx, id); err != nil {
		return err
	}
	if sk != nil {
		base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
		if base != "" {
			skillDir := filepath.Join(base, sk.Name)
			if err := os.RemoveAll(skillDir); err != nil {
				// Log but don't fail — DB row is already gone; dir is orphaned but harmless.
				_ = err
			}
		}
	}
	return nil
}

// SyncAllToDisk materializes every skill in the DB to disk, writing only files
// that are absent on disk. Called once at startup to bring pre-DiskSyncStore
// DB-only skills onto the filesystem.
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
		skillDir := filepath.Join(base, sk.Name)
		paths, err := d.ListFiles(ctx, sk.ID)
		if err != nil {
			return fmt.Errorf("disk_sync sync_all: list files for %q: %w", sk.Name, err)
		}
		for _, p := range paths {
			diskPath := filepath.Join(skillDir, filepath.FromSlash(p))
			if _, statErr := os.Stat(diskPath); statErr == nil {
				continue // already on disk
			}
			content, err := d.LoadFile(ctx, sk.ID, p)
			if err != nil {
				return fmt.Errorf("disk_sync sync_all: load %q in %q: %w", p, sk.Name, err)
			}
			if err := writeFile(diskPath, content); err != nil {
				return fmt.Errorf("disk_sync sync_all: write %q in %q: %w", p, sk.Name, err)
			}
		}
	}
	return nil
}

func (d *DiskSyncStore) ExpireDrafts(ctx context.Context, before time.Time) error {
	return d.Store.ExpireDrafts(ctx, before)
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
	skillDir := filepath.Join(base, skillName)
	for p, content := range files {
		diskPath := filepath.Join(skillDir, filepath.FromSlash(p))
		if err := writeFile(diskPath, content); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
