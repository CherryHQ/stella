package asset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/blob"
)

type memBlobStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMemBlobStore() *memBlobStore { return &memBlobStore{objs: make(map[string][]byte)} }
func (m *memBlobStore) Put(_ context.Context, key string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := io.ReadAll(r)
	if err == nil {
		m.objs[key] = data
	}
	return err
}

func (m *memBlobStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *memBlobStore) Delete(context.Context, string) error           { return nil }
func (m *memBlobStore) List(context.Context, string) ([]string, error) { return nil, nil }

func mustStore(t *testing.T, home string, blobs blob.Store) *Store {
	t.Helper()
	s, err := NewStore(home, blobs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionMediaLocalPutOpenAndIntegrity(t *testing.T) {
	home := t.TempDir()
	media := mustStore(t, home, nil).SessionMedia()
	owner := UserMediaOwner(uuid.New())
	data := []byte("immutable pixels")
	digest := sha256.Sum256(data)
	wrong := sha256.Sum256([]byte("wrong"))

	if err := media.PutSessionMedia(context.Background(), owner, wrong, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("wrong digest: %v", err)
	}
	if err := media.PutSessionMedia(context.Background(), owner, digest, data); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := media.OpenSessionMedia(context.Background(), owner, digest, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("open = %q, %v", got, err)
	}

	path := filepath.Join(home, filepath.FromSlash(sessionMediaKey(owner, digest)))
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := media.OpenSessionMedia(context.Background(), owner, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("tampered open: %v", err)
	}
	if err := media.PutSessionMedia(context.Background(), owner, digest, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("put over poisoned object: %v", err)
	}
}

func TestSessionMediaBlobStorePutOpenAndIntegrity(t *testing.T) {
	blobs := newMemBlobStore()
	media := mustStore(t, t.TempDir(), blobs).SessionMedia()
	owner := UserMediaOwner(uuid.New())
	data := []byte("immutable remote pixels")
	digest := sha256.Sum256(data)
	key := sessionMediaKey(owner, digest)

	if err := media.PutSessionMedia(context.Background(), owner, digest, data); err != nil {
		t.Fatal(err)
	}
	got, err := media.OpenSessionMedia(context.Background(), owner, digest, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("open = %q, %v", got, err)
	}
	blobs.objs[key] = []byte("tampered")
	if _, err := media.OpenSessionMedia(context.Background(), owner, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("tampered blob open: %v", err)
	}
}

func TestNewStoreRequiresHome(t *testing.T) {
	if _, err := NewStore("", nil, nil); err == nil {
		t.Fatal("expected empty home error")
	}
}

// A group owns its media the same way a user does, but never in the same tree:
// the kind, not just the UUID, selects the prefix, so two owners that somehow
// shared an ID still could not read each other's objects.
func TestSessionMediaOwnerKindSeparatesStorage(t *testing.T) {
	home := t.TempDir()
	media := mustStore(t, home, nil).SessionMedia()
	id := uuid.New()
	user, group := UserMediaOwner(id), GroupMediaOwner(id)
	data := []byte("group pixels")
	digest := sha256.Sum256(data)

	if got := sessionMediaKey(user, digest); !strings.HasPrefix(got, "users/"+id.String()+"/session-media/") {
		t.Fatalf("user key = %q", got)
	}
	if got := sessionMediaKey(group, digest); !strings.HasPrefix(got, "groups/"+id.String()+"/session-media/") {
		t.Fatalf("group key = %q", got)
	}
	if err := media.PutSessionMedia(context.Background(), group, digest, data); err != nil {
		t.Fatalf("put group media: %v", err)
	}
	if got, err := media.OpenSessionMedia(context.Background(), group, digest, int64(len(data))); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("open group media = %q, %v", got, err)
	}
	if _, err := media.OpenSessionMedia(context.Background(), user, digest, int64(len(data))); err == nil {
		t.Fatal("user owner resolved group media")
	}
}

func TestSessionMediaRejectsInvalidOwner(t *testing.T) {
	media := mustStore(t, t.TempDir(), nil).SessionMedia()
	data := []byte("pixels")
	digest := sha256.Sum256(data)
	for name, owner := range map[string]MediaOwner{
		"zero":    {},
		"no kind": {ID: uuid.New()},
		"nil id":  {Kind: OwnerUser},
	} {
		if err := media.PutSessionMedia(context.Background(), owner, digest, data); !errors.Is(err, ErrSessionMediaIntegrity) {
			t.Fatalf("%s put: %v", name, err)
		}
		if _, err := media.OpenSessionMedia(context.Background(), owner, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
			t.Fatalf("%s open: %v", name, err)
		}
	}
}
