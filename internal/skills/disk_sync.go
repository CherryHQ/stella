package skills

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// PathResolver maps a skill's (scope, agentID, userID) to the base directory
// where the skill's subdirectory lives on disk (e.g. "$STELLA_HOME/.agents/skills").
// Return empty string to skip disk mirroring for a given scope.
type PathResolver func(scope, agentID string, userID string) string

// DiskSyncStore wraps a Store and mirrors every write to disk so that skill
// scripts can be executed from the filesystem inside a sandbox.
//
// Create ordering is disk-first: if the disk write fails the DB write is skipped
// and the error is propagated. On crash after disk write but before DB write,
// the create is lost and the directory lingers as an inert orphan — nothing
// re-imports disk files into the DB, and SyncAllToDisk only visits DB skills.
//
// Reflect patch ordering is DB-first because the expected-version check is only
// authoritative inside the inner store's row lock. Disk is mirrored only after
// the DB accepts the patch.
//
// For Delete, DB is removed first (disk after): a crash then leaves an inert
// orphan directory, whereas disk-first would leave a live DB row that load
// re-materializes — deleting the skill would silently fail.
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

func (d *DiskSyncStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	files := map[string]string{MainFile: in.MainFileContent}
	base := d.resolver("user_agent", in.AgentID, in.UserID)
	if base != "" {
		if err := d.writeFilesToDisk(base, in.Name, files); err != nil {
			return Skill{}, fmt.Errorf("disk_sync reflect create %q: %w", in.Name, err)
		}
	}
	return d.Store.CreateReflectOwnedUserAgentSkill(ctx, in)
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

func (d *DiskSyncStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	patched, err := d.Store.PatchReflectOwnedUserAgentSkill(ctx, in)
	if err != nil {
		return Skill{}, err
	}
	if in.MainFileContent != nil {
		base := d.resolver(patched.Scope, patched.AgentID, patched.UserID)
		if base != "" {
			diskPath, err := safeDiskPath(base, patched.Name, filepath.FromSlash(MainFile))
			if err != nil {
				return Skill{}, fmt.Errorf("disk_sync reflect patch %q: %w", patched.Name, err)
			}
			if err := writeFile(diskPath, *in.MainFileContent); err != nil {
				return Skill{}, fmt.Errorf("disk_sync reflect patch %q: %w", patched.Name, err)
			}
		}
	}
	return patched, nil
}

func (d *DiskSyncStore) RestoreReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillRestore) (ReflectSkillRestoreResult, error) {
	// Deprecating a Reflect skill only changes DB status and usage rows, so restore
	// does not need extra disk writes; the skill files should already be present.
	return d.Store.RestoreReflectOwnedUserAgentSkill(ctx, in)
}

// DeprecateManagedSkill changes the DB lifecycle state before removing its
// mirror. A failed transaction therefore leaves the executable mirror intact.
func (d *DiskSyncStore) DeprecateManagedSkill(ctx context.Context, in ManagedSkillDeprecate) (Skill, error) {
	// A previous attempt may have committed the DB deprecation before its mirror
	// cleanup failed. Retry that cleanup without touching version or changelog.
	existing, err := d.findByID(ctx, in.ID)
	if err != nil {
		return Skill{}, err
	}
	if existing != nil && existing.Status == "deprecated" {
		if err := validateDeprecatedMirrorRetry(*existing, in); err != nil {
			return Skill{}, err
		}
		if err := d.removeSkillDir(ctx, existing); err != nil {
			return *existing, fmt.Errorf("disk_sync deprecate %q: remove committed mirror: %w", existing.Name, err)
		}
		return *existing, nil
	}

	deprecated, err := d.Store.DeprecateManagedSkill(ctx, in)
	if err != nil {
		return Skill{}, err
	}
	if err := d.removeSkillDir(ctx, &deprecated); err != nil {
		// The DB transaction is already committed and must remain deprecated.
		return deprecated, fmt.Errorf("disk_sync deprecate %q: remove committed mirror: %w", deprecated.Name, err)
	}
	return deprecated, nil
}

// RestoreManagedSkill rebuilds the mirror from DB-retained files after the
// lifecycle transaction commits, including idempotent active-row restores.
func (d *DiskSyncStore) RestoreManagedSkill(ctx context.Context, in ManagedSkillRestore) (ManagedSkillRestoreResult, error) {
	result, err := d.Store.RestoreManagedSkill(ctx, in)
	if err != nil {
		return ManagedSkillRestoreResult{}, err
	}
	if err := d.materializeSkill(ctx, result.Skill); err != nil {
		return ManagedSkillRestoreResult{}, err
	}
	return result, nil
}

// UpdateManagedSkill mirrors all retained DB files only after its atomic DB
// update succeeds, preventing an uncommitted file body from reaching disk.
func (d *DiskSyncStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (Skill, error) {
	updated, err := d.Store.UpdateManagedSkill(ctx, in)
	if err != nil {
		return Skill{}, err
	}
	if err := d.materializeSkill(ctx, updated); err != nil {
		return Skill{}, err
	}
	return updated, nil
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

// Delete removes the DB row first, then removes the disk directory; a crash
// in between leaves an inert orphan dir rather than a live, undeletable skill.
func (d *DiskSyncStore) Delete(ctx context.Context, id string, vc ViewContext) error {
	sk, err := d.findByID(ctx, id)
	if err != nil {
		return err
	}
	if err := d.Store.Delete(ctx, id, vc); err != nil {
		return err
	}
	_ = d.removeSkillDir(ctx, sk)
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
	_ = d.removeSkillDir(ctx, sk)
	return nil
}

func (d *DiskSyncStore) removeSkillDir(ctx context.Context, sk *Skill) error {
	if sk == nil {
		return nil
	}
	base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
	if base == "" {
		return nil
	}
	skillDir, pathErr := safeDiskPath(base, sk.Name)
	if pathErr != nil {
		return fmt.Errorf("resolve mirror path for %q: %w", sk.Name, pathErr)
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("remove mirror directory %q: %w", skillDir, err)
	}
	return nil
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
		if sk.Status == "deprecated" {
			if err := d.removeSkillDir(ctx, &sk); err != nil {
				return fmt.Errorf("disk_sync sync_all: remove deprecated mirror %q: %w", sk.Name, err)
			}
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

// validateDeprecatedMirrorRetry prevents an arbitrary ID from retrying a
// mirror cleanup outside its mutable owner bucket.
func validateDeprecatedMirrorRetry(sk Skill, in ManagedSkillDeprecate) error {
	if !isMutableSkillScope(in.Scope) || sk.Scope != in.Scope || !managedOwnerMatches(sk, in.UserID, in.AgentID) {
		return ErrSkillNotMutable
	}
	return nil
}

// materializeSkill replaces a skill mirror from its committed DB file set.
func (d *DiskSyncStore) materializeSkill(ctx context.Context, sk Skill) error {
	base := d.resolver(sk.Scope, sk.AgentID, sk.UserID)
	if base == "" {
		return nil
	}
	files, err := d.ListFilesWithContent(ctx, sk.ID)
	if err != nil {
		return fmt.Errorf("disk_sync: list retained files for %q: %w", sk.Name, err)
	}
	if err := d.writeFilesToDisk(base, sk.Name, files); err != nil {
		return fmt.Errorf("disk_sync: materialize %q: %w", sk.Name, err)
	}
	skillDir, err := safeDiskPath(base, sk.Name)
	if err != nil {
		return fmt.Errorf("disk_sync: materialize path %q: %w", sk.Name, err)
	}
	removeOrphanDiskFiles(ctx, skillDir, files, sk.Name)
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
var _ Store = (*DiskSyncStore)(nil)
