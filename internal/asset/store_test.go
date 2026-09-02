package asset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/platform/blob"
)

type memBlobStore struct {
	mu        sync.Mutex
	objs      map[string][]byte
	deleteErr map[string]error
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

func (m *memBlobStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.deleteErr[key]; ok {
		return err
	}
	delete(m.objs, key)
	return nil
}

func (m *memBlobStore) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for key := range m.objs {
		if strings.HasPrefix(key, prefix+"/") {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

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

// Deleting an owner is the one bulk operation a write-once store performs, so
// it has to stop exactly at the owner's prefix. The assertions that matter are
// the survivors: another owner's objects, and this owner's own non-media tree.
func TestDeleteSessionMediaOwnerClearsOnlyThatOwner(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"local", "blob"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			var blobs *memBlobStore
			if mode == "blob" {
				blobs = newMemBlobStore()
			}
			var backing blob.Store
			if blobs != nil {
				backing = blobs
			}
			media := mustStore(t, home, backing).SessionMedia()

			doomed := UserMediaOwner(uuid.New())
			neighbour := UserMediaOwner(uuid.New())
			group := GroupMediaOwner(doomed.ID)

			put := func(owner MediaOwner, body string) [sha256.Size]byte {
				t.Helper()
				data := []byte(body)
				digest := sha256.Sum256(data)
				if err := media.PutSessionMedia(ctx, owner, digest, data); err != nil {
					t.Fatalf("put %s: %v", body, err)
				}
				return digest
			}
			doomedFirst := put(doomed, "doomed one")
			doomedSecond := put(doomed, "doomed two")
			neighbourDigest := put(neighbour, "neighbour")
			groupDigest := put(group, "same uuid, other kind")

			// A sibling tree under the same user proves the delete is scoped to
			// session-media, not to the whole principal directory.
			sibling := filepath.Join(home, "users", doomed.ID.String(), "data", "keep.txt")
			if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if blobs != nil {
				blobs.objs["users/"+doomed.ID.String()+"/data/keep.txt"] = []byte("keep")
			}

			if err := media.DeleteSessionMediaOwner(ctx, doomed); err != nil {
				t.Fatalf("delete owner: %v", err)
			}

			for _, digest := range [][sha256.Size]byte{doomedFirst, doomedSecond} {
				if _, err := media.OpenSessionMedia(ctx, doomed, digest, int64(len("doomed one"))); err == nil {
					t.Fatal("purged object still readable")
				}
			}
			if _, err := media.OpenSessionMedia(ctx, neighbour, neighbourDigest, int64(len("neighbour"))); err != nil {
				t.Fatalf("neighbour object lost: %v", err)
			}
			if _, err := media.OpenSessionMedia(ctx, group, groupDigest, int64(len("same uuid, other kind"))); err != nil {
				t.Fatalf("group object with the same UUID lost: %v", err)
			}
			if _, err := os.Stat(sibling); err != nil {
				t.Fatalf("non-media sibling tree lost: %v", err)
			}
			if blobs != nil {
				if _, ok := blobs.objs["users/"+doomed.ID.String()+"/data/keep.txt"]; !ok {
					t.Fatal("non-media blob under the same principal lost")
				}
			}

			// Purging an owner that never stored anything is a success, not a
			// missing-object error: deletion is called on every owner.
			if err := media.DeleteSessionMediaOwner(ctx, UserMediaOwner(uuid.New())); err != nil {
				t.Fatalf("delete empty owner: %v", err)
			}
			if err := media.DeleteSessionMediaOwner(ctx, MediaOwner{}); err == nil {
				t.Fatal("invalid owner accepted")
			}
		})
	}
}

// One unreachable object must not spare the rest of the tree. Nothing revisits
// a failed owner purge — the rows naming these objects are already cascaded
// away — so the delete attempts every key and reports what it could not remove.
func TestDeleteSessionMediaOwnerAttemptsEveryKey(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobStore()
	media := mustStore(t, t.TempDir(), blobs).SessionMedia()
	owner := UserMediaOwner(uuid.New())

	digests := make([][sha256.Size]byte, 0, 3)
	for _, body := range []string{"first", "second", "third"} {
		data := []byte(body)
		digest := sha256.Sum256(data)
		if err := media.PutSessionMedia(ctx, owner, digest, data); err != nil {
			t.Fatalf("put %s: %v", body, err)
		}
		digests = append(digests, digest)
	}
	// Fail the key that sorts first, so a delete that gave up on the first error
	// would leave both survivors behind.
	stuck := sessionMediaKey(owner, digests[0])
	for _, digest := range digests[1:] {
		if key := sessionMediaKey(owner, digest); key < stuck {
			stuck = key
		}
	}
	blobs.deleteErr = map[string]error{stuck: errors.New("object locked")}

	err := media.DeleteSessionMediaOwner(ctx, owner)
	if err == nil {
		t.Fatal("purge reported success despite an undeleted object")
	}
	if !strings.Contains(err.Error(), "object locked") {
		t.Fatalf("purge error = %v, want the underlying failure", err)
	}
	remaining := 0
	for key := range blobs.objs {
		if strings.HasPrefix(key, "users/"+owner.ID.String()+"/") {
			remaining++
		}
	}
	if remaining != 1 {
		t.Fatalf("%d objects left under the owner, want only the one that could not be deleted", remaining)
	}
}
