package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const nativeCleanupMarkerDir = "cache/sandbox-recovery"

type nativeCleanupMarker struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Backend   string `json:"backend"`
}

// MarkNativeCleanupPending records a session owner before a native backend
// starts a process. Repeated calls for the same session/backend are idempotent.
// Native process Wait/Close paths cannot remove this marker because descendants
// may outlive the leader. A stale marker is intentionally enough to block
// shared resource GC after a daemon restart.
func MarkNativeCleanupPending(stellaHome, sessionID, backend string) error {
	if stellaHome == "" {
		return nil
	}
	if err := validCleanupMarkerPart(sessionID); err != nil {
		return fmt.Errorf("sandbox recovery: session id: %w", err)
	}
	if err := validCleanupMarkerPart(backend); err != nil {
		return fmt.Errorf("sandbox recovery: backend: %w", err)
	}

	dir := filepath.Join(stellaHome, nativeCleanupMarkerDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sandbox recovery: create marker directory: %w", err)
	}
	if err := syncDirectory(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("sandbox recovery: sync marker parent: %w", err)
	}
	if parent := filepath.Dir(filepath.Dir(dir)); parent != filepath.Dir(dir) {
		if err := syncDirectory(parent); err != nil {
			return fmt.Errorf("sandbox recovery: sync marker root parent: %w", err)
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("sandbox recovery: protect marker directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return fmt.Errorf("sandbox recovery: create marker: %w", err)
	}
	tmpPath := tmp.Name()
	// One marker is the session-level ownership proof. Repeated native starts
	// are idempotent and must not create independent releases that could remove
	// the proof while another child is still alive.
	markerPath := filepath.Join(dir, sessionID+".json")
	removeTemp := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer func() {
		if err != nil {
			removeTemp()
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("sandbox recovery: protect marker: %w", err)
	}
	if err = json.NewEncoder(tmp).Encode(nativeCleanupMarker{Version: 1, SessionID: sessionID, Backend: backend}); err != nil {
		return fmt.Errorf("sandbox recovery: write marker: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sandbox recovery: sync marker: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("sandbox recovery: close marker: %w", err)
	}
	if err = os.Link(tmpPath, markerPath); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("sandbox recovery: publish marker: %w", err)
		}
		data, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			return fmt.Errorf("sandbox recovery: inspect existing marker: %w", readErr)
		}
		var existing nativeCleanupMarker
		if decodeErr := json.Unmarshal(data, &existing); decodeErr != nil || existing.Version != 1 || existing.SessionID != sessionID || existing.Backend != backend {
			return fmt.Errorf("sandbox recovery: existing marker does not match session")
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return fmt.Errorf("sandbox recovery: remove duplicate marker staging file: %w", removeErr)
		}
		if err = syncDirectory(dir); err != nil {
			return fmt.Errorf("sandbox recovery: sync duplicate marker directory: %w", err)
		}
		err = nil
		return nil
	}
	if err = os.Remove(tmpPath); err != nil {
		return fmt.Errorf("sandbox recovery: remove marker staging file: %w", err)
	}
	if err = syncDirectory(dir); err != nil {
		return fmt.Errorf("sandbox recovery: sync marker directory: %w", err)
	}
	return nil
}

// CleanupAllowed returns whether persistent sandbox-owned resources may be
// reclaimed. Any marker is an unresolved owner, so callers must retain all
// resources whose ownership cannot be reconstructed.
func CleanupAllowed(ctx context.Context, stellaHome string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if stellaHome == "" {
		return true, nil
	}
	dir := filepath.Join(stellaHome, nativeCleanupMarkerDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("sandbox recovery: read markers: %w", err)
	}
	// Any entry, including a malformed marker or crash-written staging file, is
	// unresolved owner evidence. The entry type/content cannot authorize
	// deletion, so only an empty directory permits cleanup.
	return len(entries) == 0, nil
}

func validCleanupMarkerPart(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return errors.New("must be one path component")
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
