// Package asset stores immutable session media outside mutable workspace and
// user-data trees. Session media is content-addressed and backed by blob.Store
// when one is configured, or by the local filesystem otherwise.
package asset

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/blob"
)

var ErrSessionMediaIntegrity = errors.New("session media integrity check failed")

// SessionMediaStore is the immutable-media port exposed to the session domain.
// It deliberately accepts identities and digests rather than paths: session
// media is never addressable through mutable workspace or user-data APIs.
type SessionMediaStore interface {
	PutSessionMedia(context.Context, uuid.UUID, [sha256.Size]byte, []byte) error
	OpenSessionMedia(context.Context, uuid.UUID, [sha256.Size]byte, int64) ([]byte, error)
}

// Store provides immutable, content-addressed session media storage. It is safe
// for concurrent use.
type Store struct {
	home      string
	blobStore blob.Store
	mu        sync.Mutex
}

// NewStore builds the immutable session media store. home is required. The
// BlobStore remains optional: configured deployments store media there, while
// deployments without one store media beneath home.
func NewStore(home string, blobStore blob.Store, _ *slog.Logger) (*Store, error) {
	if home == "" {
		return nil, fmt.Errorf("asset store: home directory is required")
	}
	return &Store{home: home, blobStore: blobStore}, nil
}

// SessionMedia returns the narrow, write-once media facet. It is intentionally
// not a path API: only this facet derives users/<id>/session-media/<sha256>.
func (s *Store) SessionMedia() SessionMediaStore { return sessionMediaStore{store: s} }

type sessionMediaStore struct{ store *Store }

func (m sessionMediaStore) PutSessionMedia(ctx context.Context, userID uuid.UUID, digest [sha256.Size]byte, data []byte) error {
	return m.store.putSessionMedia(ctx, userID, digest, data)
}

func (m sessionMediaStore) OpenSessionMedia(ctx context.Context, userID uuid.UUID, digest [sha256.Size]byte, sizeBytes int64) ([]byte, error) {
	return m.store.openSessionMedia(ctx, userID, digest, sizeBytes)
}

func (s *Store) putSessionMedia(ctx context.Context, userID uuid.UUID, digest [sha256.Size]byte, data []byte) error {
	if err := verifySessionMedia(data, digest, int64(len(data))); err != nil {
		return err
	}
	key := sessionMediaKey(userID, digest)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.blobStore != nil {
		if existing, err := s.readBlobSessionMedia(ctx, key); err == nil {
			return verifySessionMedia(existing, digest, int64(len(data)))
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("open existing session media: %w", err)
		}
		if err := s.blobStore.Put(ctx, key, bytes.NewReader(data)); err != nil {
			return fmt.Errorf("persist session media: %w", err)
		}
		stored, err := s.readBlobSessionMedia(ctx, key)
		if err != nil {
			return fmt.Errorf("verify persisted session media: %w", err)
		}
		return verifySessionMedia(stored, digest, int64(len(data)))
	}

	return s.writeLocalSessionMedia(userID, digest, data)
}

func (s *Store) openSessionMedia(ctx context.Context, userID uuid.UUID, digest [sha256.Size]byte, sizeBytes int64) ([]byte, error) {
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid size %d", ErrSessionMediaIntegrity, sizeBytes)
	}
	key := sessionMediaKey(userID, digest)

	s.mu.Lock()
	defer s.mu.Unlock()

	var data []byte
	var err error
	if s.blobStore != nil {
		data, err = s.readBlobSessionMedia(ctx, key)
	} else {
		data, err = s.readLocalSessionMedia(userID, digest)
	}
	if err != nil {
		return nil, fmt.Errorf("open session media: %w", err)
	}
	if err := verifySessionMedia(data, digest, sizeBytes); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Store) readBlobSessionMedia(ctx context.Context, key string) ([]byte, error) {
	rc, err := s.blobStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return nil, readErr
	}
	return data, closeErr
}

func (s *Store) writeLocalSessionMedia(userID uuid.UUID, digest [sha256.Size]byte, data []byte) error {
	root, rel, err := s.openPath(filepath.Join(s.home, filepath.FromSlash(sessionMediaKey(userID, digest))))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if existing, err := root.ReadFile(rel); err == nil {
		return verifySessionMedia(existing, digest, int64(len(data)))
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return err
	}

	f, tmp, err := createRootTemp(root, filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if existing, err := root.ReadFile(rel); err == nil {
		return verifySessionMedia(existing, digest, int64(len(data)))
	} else if !os.IsNotExist(err) {
		return err
	}
	return root.Rename(tmp, rel)
}

func (s *Store) readLocalSessionMedia(userID uuid.UUID, digest [sha256.Size]byte) ([]byte, error) {
	root, rel, err := s.openPath(filepath.Join(s.home, filepath.FromSlash(sessionMediaKey(userID, digest))))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

func sessionMediaKey(userID uuid.UUID, digest [sha256.Size]byte) string {
	return filepath.ToSlash(filepath.Join("users", userID.String(), "session-media", hex.EncodeToString(digest[:])))
}

func verifySessionMedia(data []byte, digest [sha256.Size]byte, sizeBytes int64) error {
	if int64(len(data)) != sizeBytes || sha256.Sum256(data) != digest {
		return fmt.Errorf("%w: expected sha256 %x and %d bytes", ErrSessionMediaIntegrity, digest, sizeBytes)
	}
	return nil
}

func (s *Store) openPath(abs string) (*os.Root, string, error) {
	boundary, rel, err := s.pathBoundary(abs)
	if err != nil {
		return nil, "", err
	}
	homeRoot, err := os.OpenRoot(s.home)
	if err != nil {
		return nil, "", err
	}
	if boundary == s.home {
		return homeRoot, rel, nil
	}
	boundaryRel, err := filepath.Rel(s.home, boundary)
	if err != nil {
		_ = homeRoot.Close()
		return nil, "", err
	}
	if info, err := homeRoot.Lstat(boundaryRel); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			_ = homeRoot.Close()
			return nil, "", fmt.Errorf("session media principal boundary is a symlink: %q", boundary)
		}
	} else if os.IsNotExist(err) {
		if err := homeRoot.MkdirAll(boundaryRel, 0o755); err != nil {
			_ = homeRoot.Close()
			return nil, "", err
		}
	} else {
		_ = homeRoot.Close()
		return nil, "", err
	}
	root, err := homeRoot.OpenRoot(boundaryRel)
	_ = homeRoot.Close()
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

func (s *Store) pathBoundary(abs string) (string, string, error) {
	homeRel, err := filepath.Rel(s.home, abs)
	if err != nil || filepath.IsAbs(homeRel) || homeRel == ".." || strings.HasPrefix(homeRel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("session media path escapes home: %q", abs)
	}
	parts := strings.Split(homeRel, string(filepath.Separator))
	if len(parts) >= 3 && parts[0] == "users" {
		boundary := filepath.Join(s.home, parts[0], parts[1])
		return boundary, filepath.Join(parts[2:]...), nil
	}
	return s.home, homeRel, nil
}

func createRootTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, ".stella-asset-"+hex.EncodeToString(random[:]))
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create session media temp file: too many collisions")
}
