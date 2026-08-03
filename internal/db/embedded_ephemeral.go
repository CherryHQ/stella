package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	ephemeralRootPrefix = "stella-pg-"
	ephemeralLockName   = "owner.lock"
	ephemeralMarkerName = ".stella-test-pg.json"
	ephemeralSchema     = "stella-test-pg"
	ephemeralVersion    = 1
)

var ephemeralJanitorOnce sync.Once

// ephemeralMarker is deliberately small. The janitor recomputes data_dir from
// the candidate root; this value only proves that the marker belongs to it.
type ephemeralMarker struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	DataDir string `json:"data_dir"`
}

type ephemeralOwner struct {
	root        string
	releaseLock func()
	releaseOnce sync.Once
}

func (o *ephemeralOwner) release() {
	if o == nil {
		return
	}
	o.releaseOnce.Do(o.releaseLock)
}

func createEphemeralOwner() (*ephemeralOwner, error) {
	root, err := os.MkdirTemp("", ephemeralRootPrefix)
	if err != nil {
		return nil, fmt.Errorf("create scratch dir: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("secure scratch dir: %w", err)
	}

	owner, err := lockNewEphemeralRoot(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := writeEphemeralMarker(root); err != nil {
		owner.release()
		_ = os.RemoveAll(root)
		return nil, err
	}
	return owner, nil
}

func writeEphemeralMarker(root string) error {
	marker := ephemeralMarker{
		Schema:  ephemeralSchema,
		Version: ephemeralVersion,
		DataDir: filepath.Join(root, "data"),
	}

	f, err := os.CreateTemp(root, ".stella-test-pg-*.tmp")
	if err != nil {
		return fmt.Errorf("create ephemeral marker: %w", err)
	}
	tmpName := f.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure ephemeral marker: %w", err)
	}
	if err := json.NewEncoder(f).Encode(marker); err != nil {
		_ = f.Close()
		return fmt.Errorf("write ephemeral marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync ephemeral marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close ephemeral marker: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(root, ephemeralMarkerName)); err != nil {
		return fmt.Errorf("install ephemeral marker: %w", err)
	}
	return nil
}

func runEphemeralJanitorOnce(pgCtl string) {
	ephemeralJanitorOnce.Do(func() {
		runEphemeralJanitor(os.TempDir(), pgCtl, stopEphemeralPostgres)
	})
}
