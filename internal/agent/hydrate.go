package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/CherryHQ/stella/internal/blob"
)

// hydratedHomes marks user/group homes this process has already restored from the
// blob store. A cold pod restores each principal at most once: the local assets
// tree is authoritative once seeded, so re-listing the mirror on every session
// setup would be wasted work. Markers last the process lifetime, except that a
// failed List or a partially failed restore pass releases its marker so the
// next session setup can retry what this one could not restore.
var hydratedHomes sync.Map // userHome -> struct{}

// HydrateUserAssets restores a principal's assets subtree from the durable blob
// mirror into its local home, so a freshly scheduled pod with an empty assets
// tree presents the user's files to sandboxed agents (which read assets through
// filesystem mounts, with no server mediation to trigger the restore-on-miss
// path). userHome is the resolved home (a user or a group home); the assets
// prefix is derived from it, so group homes under users/group-... hydrate the
// same way without hardcoding the users/ shape.
//
// No-ops when no blob store is configured. Single-flight per home within the
// process. The local tree is authoritative: an asset that already exists on disk
// is never overwritten. All per-file failures warn and continue; only a List
// failure is returned, since it means the caller learned nothing about the
// subtree.
func HydrateUserAssets(ctx context.Context, stellaHome, userHome string) error {
	store := blob.Default()
	if store == nil {
		return nil
	}
	if _, loaded := hydratedHomes.LoadOrStore(userHome, struct{}{}); loaded {
		return nil
	}

	// Derive the assets prefix from the actual home path so a group home hydrates
	// from users/group-{id}/data/assets/ and a user home from users/{id}/data/assets/.
	prefix, err := blob.KeyForPath(stellaHome, UserAssetsDir(userHome))
	if err != nil {
		hydratedHomes.Delete(userHome)
		return err
	}
	keys, err := store.List(ctx, prefix)
	if err != nil {
		// A failed List learned nothing about the subtree; release the marker so
		// the next session setup retries instead of skipping this home for the
		// rest of the process lifetime.
		hydratedHomes.Delete(userHome)
		return err
	}

	restored, failed := 0, 0
	for _, rawKey := range keys {
		// Re-validate listed keys before writing to disk. The FS backend derives
		// keys from a confined walk, but an S3 listing echoes whatever object
		// names the bucket holds — defense in depth against a traversal key
		// planted out of band, matching the server restore path's posture.
		key, err := blob.ValidateKey(rawKey)
		if err != nil {
			slog.Warn("asset hydration skipping invalid key", "key", rawKey, "error", err)
			continue
		}
		abs := filepath.Join(stellaHome, filepath.FromSlash(key))
		if _, err := os.Stat(abs); err == nil {
			continue // local file wins; never overwrite
		} else if !os.IsNotExist(err) {
			slog.Warn("asset hydration stat failed", "key", key, "error", err)
			failed++
			continue
		}
		if err := restoreAssetKey(ctx, store, key, abs); err != nil {
			slog.Warn("asset hydration restore failed", "key", key, "error", err)
			failed++
			continue
		}
		restored++
	}
	if failed > 0 {
		// Sandbox agents read these files straight off the mount — there is no
		// restore-on-miss backstop for them. Release the marker so the next
		// session setup retries the files this pass could not restore.
		hydratedHomes.Delete(userHome)
	}
	if restored > 0 || failed > 0 {
		slog.Info("hydrated user assets from blob store", "home", userHome, "restored", restored, "failed", failed, "total", len(keys))
	}
	return nil
}

// resetHydrationForTest clears the process-wide single-flight markers so a test
// can drive HydrateUserAssets against a fresh home.
func resetHydrationForTest() {
	hydratedHomes.Range(func(k, _ any) bool {
		hydratedHomes.Delete(k)
		return true
	})
}

// restoreAssetKey copies one blob key to abs via a temp file in the target dir,
// chmod 0644, then an atomic no-replace install — the same write-back pattern
// as the server's restore-on-miss handler, so both restore paths land identical
// file modes and both let a concurrently written local file win.
func restoreAssetKey(ctx context.Context, store blob.Store, key, abs string) error {
	rc, err := store.Open(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".stella-hydrate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		return err
	}
	// Normalize to 0644, matching writeRestoredAssetFile: channel SaveAsset writes
	// 0600, but per-user home files do not use mode bits as a security boundary.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Install with no-replace semantics: rename would silently clobber a file
	// written between the caller's missing-file check and this install, replacing
	// fresh local content with older blob content. link(2) fails with EEXIST
	// instead (on Windows too), so the concurrent local write wins.
	if err := os.Link(tmpName, abs); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return nil
}
