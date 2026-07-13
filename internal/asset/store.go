// Package asset is the authoritative persistence boundary for uploaded user
// assets. It owns durable writes, local materialization, restore-on-miss, move,
// delete, and cold-pod hydration, so transports and channel plugins never touch
// a raw blob.Store or a process-global mirror.
//
// Two deployment authorities exist:
//
//   - Single-node: the local filesystem under home is itself the durable
//     authority. There is no separate object store; a local write is durable.
//   - Multi-replica: a shared object store is the durable authority and the local
//     filesystem is a materialization cache. A write is durable only once it
//     reaches the object store, so a failed authority write fails the operation —
//     an asset is never silently local-only. A newly scheduled pod restores the
//     files it lacks from the authority (lazily on read-miss, eagerly per user).
//
// Multi-replica deployment validation belongs to the one deployment mechanism
// that enables that topology; AssetStore executes the selected authority's
// semantics without a second, contradictory mode switch.
package asset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/blob"
)

// Store is the asset persistence service. It is safe for concurrent use.
type Store struct {
	// home is the root all durable keys are relative to (STELLA_HOME). Local
	// materializations and object keys both derive from it.
	home string
	// authority is the shared durable object store. A nil authority means the
	// local filesystem is the durable authority (single-node), so the durable
	// operations below become no-ops: the local file the caller already wrote is
	// itself durable.
	authority blob.Store
	log       *slog.Logger

	// hydrated marks per-user asset trees this process has already restored from
	// the authority, so a cold pod restores each user at most once.
	hydrated sync.Map // assetsDir -> struct{}
}

// NewStore builds the asset store. home is required. A configured object store
// is the shared durable authority; otherwise the local filesystem is the
// single-node authority. There is deliberately no independent mode flag that
// can disagree with the selected authority.
func NewStore(home string, authority blob.Store, log *slog.Logger) (*Store, error) {
	if home == "" {
		return nil, fmt.Errorf("asset store: home directory is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Store{home: home, authority: authority, log: log}, nil
}

// SharedAuthority reports whether a shared object store backs this store. It is
// true for multi-replica deployments and false when the local filesystem is the
// authority. Tests and diagnostics use it; production code should not branch on
// it — the durable operations already encode the authority choice.
func (s *Store) SharedAuthority() bool { return s.authority != nil }

// SaveAsset writes data as a new timestamped file under assetsDir, durably
// persists it in the selected authority, and returns the local materialized
// path. It replaces the former package-global agent.SaveAsset: the durable write
// is no longer best-effort — a shared-authority failure fails the call and the
// local orphan it just created is removed, so a reported failure never leaves a
// local-only "success" that a channel would treat as saved.
func (s *Store) SaveAsset(ctx context.Context, assetsDir, fileName string, data []byte) (string, error) {
	name := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(fileName))
	dst := filepath.Join(assetsDir, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write asset: %w", err)
	}
	if err := s.persist(ctx, dst, data); err != nil {
		_ = os.Remove(dst) // the timestamped name is always new; drop the orphan
		return "", err
	}
	return dst, nil
}

// CreateFile creates a file at abs with the given content, materializing it
// locally and durably persisting it when abs is a user asset. It is atomic with
// respect to the durable authority: if the durable write fails, the local file
// this call created is removed (or a pre-existing file restored), so a reported
// failure never leaves a local-only asset. Non-asset paths are written locally
// only. It is the create-time entry point for CreateWorkspaceFile; the underlying
// write matches WriteFile's overwrite-and-rollback semantics.
func (s *Store) CreateFile(ctx context.Context, abs string, content []byte, perm os.FileMode) error {
	return s.WriteFile(ctx, abs, content, perm)
}

// WriteFile materializes data at abs locally (creating parent directories, mode
// perm) and durably persists it in the shared authority when abs is a user
// asset. The local write is atomic with the durable write: if the durable write
// fails, a newly-created file is removed and a pre-existing file is restored to
// its prior content, so the local state always matches the durable outcome and a
// reported failure never leaves a local-only or half-updated asset. Non-asset
// paths are written locally only (rebuildable workspace state). With no shared
// authority the local write is itself durable.
func (s *Store) WriteFile(ctx context.Context, abs string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	// Snapshot the prior content only when a durable write could actually fail
	// (an asset path with a shared authority); a non-asset or single-node write
	// never needs a rollback, so skip the read.
	var (
		prior    []byte
		hadPrior bool
		willP    = s.willPersist(abs)
	)
	if willP {
		if b, err := os.ReadFile(abs); err == nil {
			prior, hadPrior = b, true
		}
	}
	if err := os.WriteFile(abs, data, perm); err != nil {
		return err
	}
	if err := s.persist(ctx, abs, data); err != nil {
		if hadPrior {
			_ = os.WriteFile(abs, prior, perm) // restore prior content
		} else {
			_ = os.Remove(abs) // remove the orphan we just created
		}
		return err
	}
	return nil
}

// MoveFile relocates src to dst, owning both the local rename and the durable
// authority so the caller is never left in a locally-moved-but-durably-
// inconsistent state. The destination is persisted to the authority in full
// BEFORE the source keys are deleted. On any authority failure — a destination
// write OR the source cleanup — the whole move is rolled back: the destination
// keys written so far are best-effort removed, the local rename is undone, and
// the source authority is re-asserted. This is required because once the local
// rename stands there is no source to retry from; rolling back restores the
// original state so the caller can retry once the authority recovers. Directories
// are moved whole. With no shared authority the local rename is itself durable.
func (s *Store) MoveFile(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	if s.authority == nil {
		return nil
	}
	written, err := s.persistTree(ctx, dst)
	if err != nil {
		// Source authority was never touched; undo the destination writes and the
		// local rename so the original state is fully restored.
		s.rollbackMove(ctx, src, dst, written)
		return fmt.Errorf("move asset: persist destination to shared authority: %w", err)
	}
	if err := s.deleteTree(ctx, src); err != nil {
		// The local rename has no retryable source once it stands, so a failed
		// source cleanup is a move failure: roll the whole move back to the source.
		s.rollbackMove(ctx, src, dst, written)
		return fmt.Errorf("move asset: source authority cleanup failed, move rolled back: %w", err)
	}
	return nil
}

// rollbackMove undoes a failed move: it renames dst back to src locally, deletes
// the destination authority keys written so far, and re-asserts the source
// authority from the restored local content (idempotent) so a partially-deleted
// source is repaired. Every authority step is best-effort — a still-unreachable
// authority leaves local correctly at src and the operation retryable once the
// authority recovers.
func (s *Store) rollbackMove(ctx context.Context, src, dst string, written []string) {
	_ = os.Rename(dst, src)
	for _, k := range written {
		_ = s.authority.Delete(ctx, k)
	}
	_, _ = s.persistTree(ctx, src)
}

// RemoveFile deletes abs locally and removes its durable authority keys. The
// authority keys are deleted BEFORE the local file so a concurrent hydration
// cannot observe the local file missing while the key still exists and restore
// it. Directories delete every child key under the prefix. A durable-authority
// failure fails the call before touching local state, leaving it retry-safe.
// With no shared authority only the local removal happens.
func (s *Store) RemoveFile(ctx context.Context, abs string) error {
	if s.authority != nil {
		if err := s.deleteTree(ctx, abs); err != nil {
			return fmt.Errorf("delete asset in shared authority: %w", err)
		}
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	return nil
}

// willPersist reports whether a write to abs would reach the shared authority
// (an asset path with a shared authority configured), i.e. whether a durable
// write could fail and require a local rollback.
func (s *Store) willPersist(abs string) bool {
	if s.authority == nil {
		return false
	}
	_, ok := s.assetKey(abs)
	return ok
}

// persist durably records a single already-materialized asset file. No-op
// without a shared authority (local is durable) or for a non-asset path.
func (s *Store) persist(ctx context.Context, abs string, data []byte) error {
	if s.authority == nil {
		return nil
	}
	key, ok := s.assetKey(abs)
	if !ok {
		return nil
	}
	if err := s.authority.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("persist asset to shared authority: %w", err)
	}
	return nil
}

// persistTree writes every user-asset file under the local path root to the
// shared authority and returns the keys it wrote (so a caller can undo them on a
// later failure). root may be a single file or a directory; non-asset files are
// skipped. It stops and returns the keys written so far on the first Put failure.
func (s *Store) persistTree(ctx context.Context, root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	var written []string
	putOne := func(p string) error {
		key, ok := s.assetKey(p)
		if !ok {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := s.authority.Put(ctx, key, bytes.NewReader(data)); err != nil {
			return err
		}
		written = append(written, key)
		return nil
	}
	if !info.IsDir() {
		if err := putOne(root); err != nil {
			return written, err
		}
		return written, nil
	}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return putOne(p)
	})
	return written, walkErr
}

// deleteTree removes every durable authority key at or under the asset path abs.
// It lists the authority under the path's key (which yields the exact object for
// a file or every child for a directory) and deletes each; if the listing is
// empty it deletes the exact key directly, covering an object store whose
// prefix listing does not echo a single leaf key. Listed keys are re-validated
// before deletion as defense against a traversal key echoed by the backend.
// No-op for a non-asset path.
func (s *Store) deleteTree(ctx context.Context, abs string) error {
	key, ok := s.assetKey(abs)
	if !ok {
		return nil
	}
	keys, err := s.authority.List(ctx, key)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return s.authority.Delete(ctx, key)
	}
	var errs []error
	for _, k := range keys {
		vk, verr := blob.ValidateKey(k)
		if verr != nil {
			continue
		}
		if err := s.authority.Delete(ctx, vk); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Restore materializes abs from the shared authority when the local file is
// missing, using a no-replace install so a file written concurrently by the
// local process wins. It returns os.ErrNotExist when no shared authority is
// configured or abs is not a user-asset path, so a caller can treat a nil return
// as "the local file now exists" and any error as "still missing". Callers must
// re-stat abs after a nil return.
func (s *Store) Restore(ctx context.Context, abs string) error {
	if s.authority == nil {
		return os.ErrNotExist
	}
	key, ok := s.assetKey(abs)
	if !ok {
		return os.ErrNotExist
	}
	return s.restoreKey(ctx, key, abs)
}

// HydrateUser eagerly restores a user's whole asset subtree from the shared
// authority into assetsDir, so a freshly scheduled pod presents the user's files
// to sandboxed agents (which read assets straight off filesystem mounts, with no
// server mediation to trigger a restore-on-miss). It is single-flight per
// assetsDir within the process and no-op without a shared authority. The local
// tree is authoritative once seeded: an asset already on disk is never
// overwritten. Per-file failures are logged and the marker is released so the
// next call retries; only a List failure is returned.
func (s *Store) HydrateUser(ctx context.Context, assetsDir string) error {
	if s.authority == nil {
		return nil
	}
	if _, loaded := s.hydrated.LoadOrStore(assetsDir, struct{}{}); loaded {
		return nil
	}
	prefix, err := blob.KeyForPath(s.home, assetsDir)
	if err != nil {
		s.hydrated.Delete(assetsDir)
		return err
	}
	keys, err := s.authority.List(ctx, prefix)
	if err != nil {
		// A failed List learned nothing about the subtree; release the marker so a
		// later call retries instead of skipping this user for the process lifetime.
		s.hydrated.Delete(assetsDir)
		return err
	}
	restored, failed := 0, 0
	for _, rawKey := range keys {
		// Re-validate listed keys before touching disk: the FS backend derives keys
		// from a confined walk, but an object listing echoes whatever names the
		// bucket holds — defense in depth against an out-of-band traversal key.
		key, err := blob.ValidateKey(rawKey)
		if err != nil {
			s.log.Warn("asset hydration skipping invalid key", "key", rawKey, "error", err)
			continue
		}
		abs := filepath.Join(s.home, filepath.FromSlash(key))
		if _, err := os.Stat(abs); err == nil {
			continue // local file wins; never overwrite
		} else if !os.IsNotExist(err) {
			s.log.Warn("asset hydration stat failed", "key", key, "error", err)
			failed++
			continue
		}
		if err := s.restoreKey(ctx, key, abs); err != nil {
			s.log.Warn("asset hydration restore failed", "key", key, "error", err)
			failed++
			continue
		}
		restored++
	}
	if failed > 0 {
		s.hydrated.Delete(assetsDir)
	}
	if restored > 0 || failed > 0 {
		s.log.Info("hydrated user assets from shared authority", "dir", assetsDir, "restored", restored, "failed", failed, "total", len(keys))
	}
	return nil
}

// resetHydrationForTest clears the single-flight markers so a test can drive
// HydrateUser against a fresh tree.
func (s *Store) resetHydrationForTest() {
	s.hydrated.Range(func(k, _ any) bool {
		s.hydrated.Delete(k)
		return true
	})
}

// assetKey returns the durable key for abs and whether abs is a user-asset path.
// Only user assets (users/.../data/assets/...) are mirrored to the authority;
// agent/project workspace files are rebuildable execution state.
func (s *Store) assetKey(abs string) (string, bool) {
	key, err := blob.KeyForPath(s.home, abs)
	if err != nil || !blob.IsUserAssetKey(key) {
		return "", false
	}
	return key, true
}

// restoreKey copies one authority key to abs via a temp file in the target dir,
// chmod 0644, then an atomic no-replace install: os.Link fails with EEXIST if a
// file appeared since the miss check, so a concurrent local write wins and the
// caller re-reads the newer local content.
func (s *Store) restoreKey(ctx context.Context, key, abs string) error {
	rc, err := s.authority.Open(ctx, key)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(rc)
	_ = rc.Close()
	if readErr != nil {
		// Read the whole object before touching disk so a miss (or a lazily-failing
		// backend) never leaves an empty asset directory behind.
		return readErr
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".stella-asset-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// Normalize restored assets to 0644: channel SaveAsset writes 0600, but files
	// under a per-user home do not use mode bits as a security boundary.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, abs); err != nil {
		if os.IsExist(err) {
			return nil // concurrent local write wins
		}
		return err
	}
	return nil
}
