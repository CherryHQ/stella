package blob

import (
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Store mirrors durable user assets by STELLA_HOME-relative slash keys.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

var (
	defaultMu    sync.RWMutex
	defaultStore Store
	defaultSet   bool
)

// SetDefault installs the process-wide blob mirror. Channel plugins currently
// create asset files from callbacks that have no dependency-injection path back
// to server setup, so SaveAsset reads this one hook instead of threading a store
// through every channel adapter. It is intentionally set-once in production.
func SetDefault(store Store) error {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultSet {
		return fmt.Errorf("blob default store already set")
	}
	defaultStore = store
	defaultSet = true
	return nil
}

// Default returns the process-wide blob mirror, or nil when none is configured.
func Default() Store {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultStore
}

// ResetDefaultForTest clears the process-wide hook for isolated tests.
func ResetDefaultForTest() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultStore = nil
	defaultSet = false
}

// KeyForPath returns the slash-separated key for abs relative to stellaHome.
func KeyForPath(stellaHome, abs string) (string, error) {
	rel, err := filepath.Rel(stellaHome, abs)
	if err != nil {
		return "", err
	}
	return ValidateKey(filepath.ToSlash(rel))
}

// ValidateKey rejects absolute and traversal keys while preserving slash paths.
func ValidateKey(key string) (string, error) {
	key = strings.TrimSpace(filepath.ToSlash(key))
	if key == "" || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	if slices.Contains(strings.Split(key, "/"), "..") {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	clean := path.Clean(key)
	if clean == "." {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	return clean, nil
}

func IsUserAssetKey(key string) bool {
	key = filepath.ToSlash(key)
	return strings.HasPrefix(key, "users/") && strings.Contains(key, "/data/assets/")
}
