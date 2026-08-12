package asset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	userID := uuid.New()
	data := []byte("immutable pixels")
	digest := sha256.Sum256(data)
	wrong := sha256.Sum256([]byte("wrong"))

	if err := media.PutSessionMedia(context.Background(), userID, wrong, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("wrong digest: %v", err)
	}
	if err := media.PutSessionMedia(context.Background(), userID, digest, data); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("open = %q, %v", got, err)
	}

	path := filepath.Join(home, filepath.FromSlash(sessionMediaKey(userID, digest)))
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("tampered open: %v", err)
	}
	if err := media.PutSessionMedia(context.Background(), userID, digest, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("put over poisoned object: %v", err)
	}
}

func TestSessionMediaBlobStorePutOpenAndIntegrity(t *testing.T) {
	blobs := newMemBlobStore()
	media := mustStore(t, t.TempDir(), blobs).SessionMedia()
	userID := uuid.New()
	data := []byte("immutable remote pixels")
	digest := sha256.Sum256(data)
	key := sessionMediaKey(userID, digest)

	if err := media.PutSessionMedia(context.Background(), userID, digest, data); err != nil {
		t.Fatal(err)
	}
	got, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("open = %q, %v", got, err)
	}
	blobs.objs[key] = []byte("tampered")
	if _, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("tampered blob open: %v", err)
	}
}

func TestNewStoreRequiresHome(t *testing.T) {
	if _, err := NewStore("", nil, nil); err == nil {
		t.Fatal("expected empty home error")
	}
}
