// Package asset persists immutable session media. Mutable user assets are owned
// directly by Home after the migration gate; this package deliberately has no
// workspace-path or hydration API.
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
	"os"
	"path/filepath"
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

// Store implements write-once session media over either the local filesystem or
// a configured blob authority. Its local and blob keys are content-addressed.
type Store struct {
	home      string
	authority blob.Store
	mu        sync.Mutex
}

// NewStore builds the immutable media store. home is required. When authority
// is nil, local storage is the authority; otherwise media is read and written
// directly through the configured blob store.
func NewStore(home string, authority blob.Store) (*Store, error) {
	if home == "" {
		return nil, fmt.Errorf("asset store: home directory is required")
	}
	return &Store{home: home, authority: authority}, nil
}

// SessionMedia returns the narrow, write-once media facet.
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

	if s.authority != nil {
		if existing, err := s.readAuthoritySessionMedia(ctx, key); err == nil {
			return verifySessionMedia(existing, digest, int64(len(data)))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("open existing session media: %w", err)
		}
		if err := s.authority.Put(ctx, key, bytes.NewReader(data)); err != nil {
			return fmt.Errorf("persist session media: %w", err)
		}
		stored, err := s.readAuthoritySessionMedia(ctx, key)
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

	var (
		data []byte
		err  error
	)
	if s.authority != nil {
		data, err = s.readAuthoritySessionMedia(ctx, key)
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

func (s *Store) readAuthoritySessionMedia(ctx context.Context, key string) ([]byte, error) {
	rc, err := s.authority.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	return data, errors.Join(readErr, closeErr)
}

func (s *Store) writeLocalSessionMedia(userID uuid.UUID, digest [sha256.Size]byte, data []byte) error {
	root, err := os.OpenRoot(s.home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	key := sessionMediaKey(userID, digest)
	if existing, err := root.ReadFile(key); err == nil {
		return verifySessionMedia(existing, digest, int64(len(data)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		return err
	}
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(key))
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if existing, err := root.ReadFile(key); err == nil {
		return verifySessionMedia(existing, digest, int64(len(data)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(tmpName, key)
}

func (s *Store) readLocalSessionMedia(userID uuid.UUID, digest [sha256.Size]byte) ([]byte, error) {
	root, err := os.OpenRoot(s.home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(sessionMediaKey(userID, digest))
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

func createRootTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, ".stella-media-"+hex.EncodeToString(random[:]))
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create session media temp file: too many collisions")
}
