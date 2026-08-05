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

// Store is a low-level object-store port keyed by validated slash paths. It is
// used by immutable session media and by the read-only legacy migration source;
// runtime mutable user assets are owned by Home. There is no process-global
// default: composition injects the selected implementation.
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
