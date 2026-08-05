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
)

type memAuthority struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMemAuthority() *memAuthority { return &memAuthority{objs: map[string][]byte{}} }
func (m *memAuthority) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = data
	return nil
}

func (m *memAuthority) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (*memAuthority) Delete(context.Context, string) error {
	return errors.New("immutable media never deletes")
}

func (*memAuthority) List(context.Context, string) ([]string, error) {
	return nil, errors.New("immutable media never lists")
}

func TestSessionMediaLocalIntegrityAndWriteOnce(t *testing.T) {
	s, err := NewStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	media := s.SessionMedia()
	userID := uuid.New()
	data := []byte("immutable pixels")
	digest := sha256.Sum256(data)
	if err := media.PutSessionMedia(context.Background(), userID, digest, data); err != nil {
		t.Fatalf("PutSessionMedia: %v", err)
	}
	if got, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data))); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("OpenSessionMedia = %q, %v", got, err)
	}
	if err := media.PutSessionMedia(context.Background(), userID, digest, []byte("different")); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("write-once mismatch = %v", err)
	}
	path := filepath.Join(s.home, filepath.FromSlash(sessionMediaKey(userID, digest)))
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("tampered media = %v", err)
	}
}

func TestSessionMediaBlobCrossInstanceIntegrity(t *testing.T) {
	authority := newMemAuthority()
	writer, err := NewStore(t.TempDir(), authority)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewStore(t.TempDir(), authority)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	data := []byte("shared immutable pixels")
	digest := sha256.Sum256(data)
	if err := writer.SessionMedia().PutSessionMedia(context.Background(), userID, digest, data); err != nil {
		t.Fatal(err)
	}
	got, err := reader.SessionMedia().OpenSessionMedia(context.Background(), userID, digest, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("cross-instance Open = %q, %v", got, err)
	}
	if err := reader.SessionMedia().PutSessionMedia(context.Background(), userID, digest, []byte("different")); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("poisoned immutable write = %v", err)
	}
}

func TestNewStoreRequiresHome(t *testing.T) {
	if _, err := NewStore("", nil); err == nil {
		t.Fatal("NewStore accepted empty home")
	}
}
