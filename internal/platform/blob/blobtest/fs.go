// Package blobtest provides a filesystem-backed blob.Store for tests.
//
// Production blob storage is S3 only (blob.NewStoreFromConfig). FSStore exists
// so tests that need a real, inspectable Store can have one without an S3
// server; it is deliberately not importable as a deployment backend. Adding a
// filesystem backend to production means moving this back into blob and giving
// it a config path, not importing blobtest.
package blobtest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/platform/blob"
)

// FSStore is a blob.Store rooted at a local directory.
type FSStore struct {
	root string
}

func NewFSStore(root string) (*FSStore, error) {
	if root == "" {
		return nil, fmt.Errorf("blob fs root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &FSStore{root: abs}, nil
}

func (s *FSStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stella-blob-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func (s *FSStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *FSStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *FSStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate the prefix through the same root-confinement path a key takes, so
	// a traversal/absolute prefix is rejected before it can widen the walk.
	dir, err := s.path(prefix)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		// A prefix that points at nothing is empty, not an error: a cold pod with
		// no assets yet, or a user who never uploaded, is the normal case.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return keys, nil
}

func (s *FSStore) path(key string) (string, error) {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("blob key escapes root: %q", key)
	}
	return path, nil
}
