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

	"github.com/CherryHQ/stella/internal/platform/blob"
)

var ErrSessionMediaIntegrity = errors.New("session media integrity check failed")

// OwnerKind names the session principal that owns a media object. A direct
// session's media belongs to its user; a group session's media belongs to the
// group, because a group's history is shared by every participant and outlives
// any one of them.
type OwnerKind string

const (
	OwnerUser  OwnerKind = "user"
	OwnerGroup OwnerKind = "group"
)

// MediaOwner is the storage principal for one media object. Its kind selects
// the blob prefix, so a group and a user with the same UUID could never read
// each other's bytes.
type MediaOwner struct {
	Kind OwnerKind
	ID   uuid.UUID
}

func UserMediaOwner(id uuid.UUID) MediaOwner  { return MediaOwner{Kind: OwnerUser, ID: id} }
func GroupMediaOwner(id uuid.UUID) MediaOwner { return MediaOwner{Kind: OwnerGroup, ID: id} }

// Valid reports whether the owner can address storage at all. An invalid owner
// is a programming error at the call site, never a missing object.
func (o MediaOwner) Valid() bool {
	return o.ID != uuid.Nil && (o.Kind == OwnerUser || o.Kind == OwnerGroup)
}

// SessionMediaStore is the immutable-media port exposed to the session domain.
// It deliberately accepts identities and digests rather than paths: session
// media is never addressable through mutable workspace or user-data APIs.
type SessionMediaStore interface {
	PutSessionMedia(context.Context, MediaOwner, [sha256.Size]byte, []byte) error
	OpenSessionMedia(context.Context, MediaOwner, [sha256.Size]byte, int64) ([]byte, error)
	// DeleteSessionMedia removes one object. Deleting is not part of the media
	// lifecycle a session sees: an object is immutable evidence for as long as
	// anything references it, and only the orphan sweep and owner deletion
	// below ever decide that nothing does.
	DeleteSessionMedia(context.Context, MediaOwner, [sha256.Size]byte) error
	// DeleteSessionMediaOwner removes every object under one owner's prefix,
	// which is the one case where enumerating the tree beats knowing its keys:
	// the rows naming them are already gone by then.
	DeleteSessionMediaOwner(context.Context, MediaOwner) error
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
// not a path API: only this facet derives <owners>/<id>/session-media/<sha256>.
func (s *Store) SessionMedia() SessionMediaStore { return sessionMediaStore{store: s} }

type sessionMediaStore struct{ store *Store }

func (m sessionMediaStore) PutSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte, data []byte) error {
	return m.store.putSessionMedia(ctx, owner, digest, data)
}

func (m sessionMediaStore) OpenSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte, sizeBytes int64) ([]byte, error) {
	return m.store.openSessionMedia(ctx, owner, digest, sizeBytes)
}

func (m sessionMediaStore) DeleteSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte) error {
	return m.store.deleteSessionMedia(ctx, owner, digest)
}

func (m sessionMediaStore) DeleteSessionMediaOwner(ctx context.Context, owner MediaOwner) error {
	return m.store.deleteSessionMediaOwner(ctx, owner)
}

func (s *Store) putSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte, data []byte) error {
	if !owner.Valid() {
		return fmt.Errorf("%w: invalid media owner", ErrSessionMediaIntegrity)
	}
	if err := verifySessionMedia(data, digest, int64(len(data))); err != nil {
		return err
	}
	key := sessionMediaKey(owner, digest)

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

	return s.writeLocalSessionMedia(owner, digest, data)
}

func (s *Store) openSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte, sizeBytes int64) ([]byte, error) {
	if !owner.Valid() {
		return nil, fmt.Errorf("%w: invalid media owner", ErrSessionMediaIntegrity)
	}
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid size %d", ErrSessionMediaIntegrity, sizeBytes)
	}
	key := sessionMediaKey(owner, digest)

	s.mu.Lock()
	defer s.mu.Unlock()

	var data []byte
	var err error
	if s.blobStore != nil {
		data, err = s.readBlobSessionMedia(ctx, key)
	} else {
		data, err = s.readLocalSessionMedia(owner, digest)
	}
	if err != nil {
		return nil, fmt.Errorf("open session media: %w", err)
	}
	if err := verifySessionMedia(data, digest, sizeBytes); err != nil {
		return nil, err
	}
	return data, nil
}

// deleteSessionMedia drops one object. An object that is already gone is a
// success: the sweeper reruns, and a retry must not stall on work it finished.
func (s *Store) deleteSessionMedia(ctx context.Context, owner MediaOwner, digest [sha256.Size]byte) error {
	if !owner.Valid() {
		return fmt.Errorf("%w: invalid media owner", ErrSessionMediaIntegrity)
	}
	key := sessionMediaKey(owner, digest)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.blobStore != nil {
		if err := s.blobStore.Delete(ctx, key); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete session media: %w", err)
		}
		return nil
	}

	root, rel, err := s.openPath(filepath.Join(s.home, filepath.FromSlash(key)))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session media: %w", err)
	}
	return nil
}

// deleteSessionMediaOwner drops one owner's whole media tree. The prefix comes
// from sessionMediaPrefix, the same derivation every key uses, so a delete can
// never reach a tree no PutSessionMedia could have written.
func (s *Store) deleteSessionMediaOwner(ctx context.Context, owner MediaOwner) error {
	if !owner.Valid() {
		return fmt.Errorf("%w: invalid media owner", ErrSessionMediaIntegrity)
	}
	prefix := sessionMediaPrefix(owner)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.blobStore != nil {
		keys, err := s.blobStore.List(ctx, prefix)
		if err != nil {
			return fmt.Errorf("list session media for delete: %w", err)
		}
		// Every key is attempted. Stopping at the first failure would leave the
		// rest of the tree behind, and nothing revisits it: the rows that named
		// these objects are already gone, so the orphan sweep cannot see them.
		var failures []error
		for _, key := range keys {
			if err := s.blobStore.Delete(ctx, key); err != nil {
				failures = append(failures, fmt.Errorf("delete session media %q: %w", key, err))
			}
		}
		return errors.Join(failures...)
	}

	root, err := os.OpenRoot(s.home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(filepath.FromSlash(prefix)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session media directory: %w", err)
	}
	return nil
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

func (s *Store) writeLocalSessionMedia(owner MediaOwner, digest [sha256.Size]byte, data []byte) error {
	root, rel, err := s.openPath(filepath.Join(s.home, filepath.FromSlash(sessionMediaKey(owner, digest))))
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

func (s *Store) readLocalSessionMedia(owner MediaOwner, digest [sha256.Size]byte) ([]byte, error) {
	root, rel, err := s.openPath(filepath.Join(s.home, filepath.FromSlash(sessionMediaKey(owner, digest))))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

// sessionMediaKey is the one place an owner becomes a storage prefix. Users and
// groups share the layout but never the tree.
func sessionMediaKey(owner MediaOwner, digest [sha256.Size]byte) string {
	return sessionMediaPrefix(owner) + "/" + hex.EncodeToString(digest[:])
}

// sessionMediaPrefix is the owner's whole media tree: every key this package
// writes lives under it, and nothing else does.
func sessionMediaPrefix(owner MediaOwner) string {
	kind := "users"
	if owner.Kind == OwnerGroup {
		kind = "groups"
	}
	return filepath.ToSlash(filepath.Join(kind, owner.ID.String(), "session-media"))
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
