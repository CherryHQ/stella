package blob

import (
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Store is the low-level object-store port for immutable blob-backed domains.
// Domain services own their keys and integrity rules; mutable workspace and
// user-data paths never use this interface. There is deliberately no
// process-global default: composition roots inject a Store into narrow domain
// services such as immutable session media and library raw content.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	// List returns every key under prefix, as slash-separated keys relative to
	// the store root. prefix is validated with the same rules as a key
	// (traversal/absolute rejected). A prefix pointing at nothing yields an
	// empty slice, not an error.
	List(ctx context.Context, prefix string) ([]string, error)
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
