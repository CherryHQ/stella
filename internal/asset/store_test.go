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
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/blob"
)

// memAuthority is an in-memory blob.Store standing in for a shared object store.
// It is deliberately independent of any local filesystem so a test can simulate a
// second replica with an empty local disk reading the same authority.
type memAuthority struct {
	mu   sync.Mutex
	objs map[string][]byte
	// failPutAfter, when >= 0, makes Put fail once this many Puts have succeeded
	// (used to inject a partial-failure mid-directory-move). -1 disables it.
	failPutAfter int
	puts         int
	failDelete   bool
	// failDeleteContains, when set, fails Delete only for keys containing it (so a
	// test can fail the source cleanup of a move without breaking the rollback).
	failDeleteContains string
	// echoKey, when set, is injected into every List result to simulate a backend
	// echoing an out-of-band (possibly traversal) key.
	echoKey string
}

func newMemAuthority() *memAuthority {
	return &memAuthority{objs: map[string][]byte{}, failPutAfter: -1}
}

func (m *memAuthority) Put(_ context.Context, key string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPutAfter >= 0 && m.puts >= m.failPutAfter {
		return errors.New("authority Put failed")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objs[key] = data
	m.puts++
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

func (m *memAuthority) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failDelete {
		return errors.New("authority Delete failed")
	}
	if m.failDeleteContains != "" && strings.Contains(key, m.failDeleteContains) {
		return errors.New("authority Delete failed for " + key)
	}
	delete(m.objs, key)
	return nil
}

func (m *memAuthority) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	// Mirror an object store's prefix semantics: a leaf object key is not echoed
	// by a "prefix/" listing; only children under the directory prefix are.
	for k := range m.objs {
		if k == prefix {
			continue
		}
		if strings.HasPrefix(k, prefix+"/") {
			keys = append(keys, k)
		}
	}
	if m.echoKey != "" && strings.HasPrefix(m.echoKey, prefix) {
		keys = append(keys, m.echoKey)
	}
	return keys, nil
}

func (m *memAuthority) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[key]
	return ok
}

func (m *memAuthority) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs)
}

type firstPutFailsAfterRelease struct {
	*memAuthority
	firstStarted chan struct{}
	releaseFirst chan struct{}
	mu           sync.Mutex
	calls        int
}

func (m *firstPutFailsAfterRelease) Put(ctx context.Context, key string, r io.Reader) error {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.firstStarted)
		<-m.releaseFirst
		return errors.New("first authority Put failed")
	}
	return m.memAuthority.Put(ctx, key, r)
}

// assetsDirFor returns the canonical per-user assets directory under home.
func assetsDirFor(home, userID string) string {
	return filepath.Join(home, "users", userID, "data", "assets")
}

func mustStore(t *testing.T, home string, authority blob.Store) *Store {
	t.Helper()
	s, err := NewStore(home, authority, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestWriteFileSerializesRollbackAgainstLaterSuccess(t *testing.T) {
	home := t.TempDir()
	abs := filepath.Join(assetsDirFor(home, "u1"), "same.txt")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := newMemAuthority()
	authority := &firstPutFailsAfterRelease{memAuthority: base, firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	s := mustStore(t, home, authority)

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.WriteFile(context.Background(), abs, []byte("B"), 0o644) }()
	<-authority.firstStarted
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.WriteFile(context.Background(), abs, []byte("C"), 0o644) }()
	select {
	case err := <-secondDone:
		t.Fatalf("later write completed before prior transition: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(authority.releaseFirst)
	if err := <-firstDone; err == nil {
		t.Fatal("first write should fail")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second write: %v", err)
	}
	local, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := blob.KeyForPath(home, abs)
	remote := base.objs[key]
	if string(local) != "C" || string(remote) != "C" {
		t.Fatalf("local=%q remote=%q, want both C", local, remote)
	}
}

func TestWriteFileCannotFollowSymlinkIntoAnotherPrincipal(t *testing.T) {
	home := t.TempDir()
	user1 := assetsDirFor(home, "u1")
	user2 := assetsDirFor(home, "u2")
	if err := os.MkdirAll(user1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(user2, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(user2, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(user1, "escape.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s := mustStore(t, home, nil)
	if err := s.WriteFile(context.Background(), link, []byte("overwritten"), 0o644); err == nil {
		t.Fatal("WriteFile followed a symlink outside the principal boundary")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "secret" {
		t.Fatalf("target=%q err=%v, want unchanged secret", got, err)
	}
}

func TestMoveFileRejectsExistingDestinationWithoutDataLoss(t *testing.T) {
	home := t.TempDir()
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(assetsDir, "source.txt")
	dst := filepath.Join(assetsDir, "destination.txt")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("destination"), 0o644); err != nil {
		t.Fatal(err)
	}
	authority := newMemAuthority()
	srcKey, _ := blob.KeyForPath(home, src)
	dstKey, _ := blob.KeyForPath(home, dst)
	authority.objs[srcKey] = []byte("source")
	authority.objs[dstKey] = []byte("destination")
	s := mustStore(t, home, authority)

	if err := s.MoveFile(context.Background(), src, dst); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("MoveFile error=%v, want ErrDestinationExists", err)
	}
	for path, want := range map[string]string{src: "source", dst: "destination"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q, %v; want %q", path, got, err, want)
		}
	}
	if string(authority.objs[srcKey]) != "source" || string(authority.objs[dstKey]) != "destination" {
		t.Fatalf("authority changed: src=%q dst=%q", authority.objs[srcKey], authority.objs[dstKey])
	}
}

func TestSessionMediaRejectsIntegrityMismatch(t *testing.T) {
	home := t.TempDir()
	s := mustStore(t, home, nil)
	media := s.SessionMedia()
	userID := uuid.New()
	data := []byte("immutable pixels")
	digest := sha256.Sum256(data)

	wrong := sha256.Sum256([]byte("different pixels"))
	if err := media.PutSessionMedia(context.Background(), userID, wrong, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("PutSessionMedia wrong digest error = %v, want ErrSessionMediaIntegrity", err)
	}
	if err := media.PutSessionMedia(context.Background(), userID, digest, data); err != nil {
		t.Fatalf("PutSessionMedia: %v", err)
	}
	path := filepath.Join(home, filepath.FromSlash(sessionMediaKey(userID, digest)))
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read session-media directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stella-asset-") {
			t.Fatalf("published session media left temporary sibling %q", entry.Name())
		}
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper local session media: %v", err)
	}
	if _, err := media.OpenSessionMedia(context.Background(), userID, digest, int64(len(data))); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("OpenSessionMedia tampered error = %v, want ErrSessionMediaIntegrity", err)
	}
	if err := media.PutSessionMedia(context.Background(), userID, digest, data); !errors.Is(err, ErrSessionMediaIntegrity) {
		t.Fatalf("PutSessionMedia must fail closed on a poisoned final object, got %v", err)
	}
}

func TestSessionMediaKeyIsOutsideSandboxAndWorkspaceRoots(t *testing.T) {
	home := t.TempDir()
	s := mustStore(t, home, nil)
	userID := uuid.New()
	digest := sha256.Sum256([]byte("pixels"))
	key := sessionMediaKey(userID, digest)
	if blob.IsUserAssetKey(key) {
		t.Fatalf("session media key %q is classified as a mutable user asset", key)
	}
	abs := filepath.Join(home, filepath.FromSlash(key))
	if _, persisted := s.assetKey(abs); persisted {
		t.Fatalf("session media path %q is accepted by the mutable asset authority", abs)
	}
	for _, mutableRoot := range []string{
		filepath.Join(home, "users", userID.String(), "data"),
		filepath.Join(home, "users", userID.String(), "agents"),
	} {
		rel, err := filepath.Rel(mutableRoot, abs)
		if err != nil {
			t.Fatalf("relative session media path: %v", err)
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("session media path %q is reachable from mutable root %q", abs, mutableRoot)
		}
	}
}

func TestNewStoreSelectsConfiguredAuthority(t *testing.T) {
	shared, err := NewStore(t.TempDir(), newMemAuthority(), nil)
	if err != nil {
		t.Fatalf("NewStore with shared authority: %v", err)
	}
	if !shared.SharedAuthority() {
		t.Fatal("store with object authority must report it as shared")
	}
	local, err := NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("NewStore with local authority: %v", err)
	}
	if local.SharedAuthority() {
		t.Fatal("store without object authority must use the local authority")
	}
	if _, err := NewStore("", nil, nil); err == nil {
		t.Fatal("expected NewStore to reject empty home")
	}
}

func TestSingleNodeIsLocalAuthority(t *testing.T) {
	home := t.TempDir()
	s := mustStore(t, home, nil)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveAsset(context.Background(), assetsDir, "note.txt", []byte("hi"))
	if err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "hi" {
		t.Fatalf("local data=%q err=%v", data, err)
	}
	// WriteFile/MoveFile/RemoveFile own the local ops even with no shared authority.
	dst := filepath.Join(assetsDir, "moved.txt")
	if err := s.MoveFile(context.Background(), p, dst); err != nil {
		t.Fatalf("MoveFile single-node: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("moved local file missing: %v", err)
	}
	if err := s.RemoveFile(context.Background(), dst); err != nil {
		t.Fatalf("RemoveFile single-node: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("removed local file still present: %v", err)
	}
	if err := s.Restore(context.Background(), p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Restore single-node err=%v, want ErrNotExist", err)
	}
	if err := s.HydrateUser(context.Background(), assetsDir); err != nil {
		t.Fatalf("HydrateUser single-node: %v", err)
	}
}

func TestSaveAssetPersistsToSharedAuthority(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveAsset(context.Background(), assetsDir, "note.txt", []byte("durable"))
	if err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	key, err := blob.KeyForPath(home, p)
	if err != nil {
		t.Fatal(err)
	}
	if data := auth.objs[key]; string(data) != "durable" {
		t.Fatalf("authority data=%q, want durable", data)
	}
}

func TestSaveAssetRemovesLocalOrphanOnAuthorityFailure(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	auth.failPutAfter = 0 // fail the first Put
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAsset(context.Background(), assetsDir, "note.txt", []byte("x")); err == nil {
		t.Fatal("expected SaveAsset to fail when the authority write fails")
	}
	// A reported failure must not leave a local-only orphan.
	entries, _ := os.ReadDir(assetsDir)
	if len(entries) != 0 {
		t.Fatalf("SaveAsset left %d local orphan(s) after a durable failure", len(entries))
	}
}

func TestWriteFileAtomicRollbackOnAuthorityFailure(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	abs := filepath.Join(assetsDirFor(home, "u1"), "note.txt")

	// New file + authority failure → orphan removed.
	auth.failPutAfter = 0
	if err := s.WriteFile(context.Background(), abs, []byte("new"), 0o644); err == nil {
		t.Fatal("expected WriteFile to fail on authority error")
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("new-file orphan not removed on failure: %v", err)
	}

	// Seed a pre-existing file (authority ok), then fail an update: the local file
	// must be atomically restored to its prior content, not left half-updated.
	auth.failPutAfter = -1
	if err := s.WriteFile(context.Background(), abs, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	auth.failPutAfter = 0
	auth.puts = 0
	if err := s.WriteFile(context.Background(), abs, []byte("v2"), 0o644); err == nil {
		t.Fatal("expected update WriteFile to fail on authority error")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("pre-existing file wrongly removed on failed update: %v", err)
	}
	if string(data) != "v1" {
		t.Fatalf("local content = %q after failed update, want the prior content v1 restored", data)
	}
}

func TestCreateFileDurableAndRollsBack(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)

	// Asset create: durable + local.
	abs := filepath.Join(assetsDirFor(home, "u1"), "made.txt")
	if err := s.CreateFile(context.Background(), abs, []byte("hello"), 0o644); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	key, _ := blob.KeyForPath(home, abs)
	if string(auth.objs[key]) != "hello" {
		t.Fatalf("authority missing created asset: %q", auth.objs[key])
	}
	if data, err := os.ReadFile(abs); err != nil || string(data) != "hello" {
		t.Fatalf("local created asset data=%q err=%v", data, err)
	}

	// Authority failure rolls back the local creation.
	abs2 := filepath.Join(assetsDirFor(home, "u1"), "failed.txt")
	auth.failPutAfter = 0
	if err := s.CreateFile(context.Background(), abs2, []byte("x"), 0o644); err == nil {
		t.Fatal("expected CreateFile to fail on authority error")
	}
	if _, err := os.Stat(abs2); !os.IsNotExist(err) {
		t.Fatalf("failed CreateFile left a local orphan: %v", err)
	}

	// Non-asset create is local only and never touches the authority.
	auth.failPutAfter = -1
	before := auth.count()
	work := filepath.Join(home, "users", "u1", "agents", "a1", "notes.txt")
	if err := s.CreateFile(context.Background(), work, []byte("code"), 0o644); err != nil {
		t.Fatalf("CreateFile non-asset: %v", err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("non-asset local file missing: %v", err)
	}
	if auth.count() != before {
		t.Fatalf("non-asset create touched the authority (count %d→%d)", before, auth.count())
	}
}

func TestNonAssetPathNotMirrored(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	work := filepath.Join(home, "users", "u1", "agents", "a1", "code.go")
	if err := s.WriteFile(context.Background(), work, []byte("package main"), 0o644); err != nil {
		t.Fatalf("WriteFile non-asset: %v", err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("non-asset local file missing: %v", err)
	}
	if auth.count() != 0 {
		t.Fatalf("authority holds %d objects, want 0 for a non-asset path", auth.count())
	}
}

// TestCrossInstanceMaterialization is the core multi-replica guarantee: instance A
// writes an asset to the shared authority; instance B, with a completely empty
// local disk, materializes the same asset on read-miss and on eager hydration.
func TestCrossInstanceMaterialization(t *testing.T) {
	auth := newMemAuthority()

	homeA := t.TempDir()
	instanceA := mustStore(t, homeA, auth)
	assetsA := assetsDirFor(homeA, "u1")
	if err := os.MkdirAll(assetsA, 0o755); err != nil {
		t.Fatal(err)
	}
	pathA, err := instanceA.SaveAsset(context.Background(), assetsA, "photo.txt", []byte("cross-instance"))
	if err != nil {
		t.Fatalf("instance A SaveAsset: %v", err)
	}
	relKey, err := blob.KeyForPath(homeA, pathA)
	if err != nil {
		t.Fatal(err)
	}

	homeB := t.TempDir()
	instanceB := mustStore(t, homeB, auth)
	absB := filepath.Join(homeB, filepath.FromSlash(relKey))
	if _, err := os.Stat(absB); !os.IsNotExist(err) {
		t.Fatalf("instance B local file should not exist yet, stat err=%v", err)
	}
	if err := instanceB.Restore(context.Background(), absB); err != nil {
		t.Fatalf("instance B Restore: %v", err)
	}
	if data, err := os.ReadFile(absB); err != nil || string(data) != "cross-instance" {
		t.Fatalf("instance B materialized data=%q err=%v", data, err)
	}

	homeC := t.TempDir()
	instanceC := mustStore(t, homeC, auth)
	if err := instanceC.HydrateUser(context.Background(), assetsDirFor(homeC, "u1")); err != nil {
		t.Fatalf("instance C HydrateUser: %v", err)
	}
	absC := filepath.Join(homeC, filepath.FromSlash(relKey))
	if data, err := os.ReadFile(absC); err != nil || string(data) != "cross-instance" {
		t.Fatalf("instance C hydrated data=%q err=%v", data, err)
	}
}

func TestRestoreDoesNotReplaceConcurrentLocalWrite(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	abs := filepath.Join(assetsDirFor(home, "u1"), "raced.txt")
	key, err := blob.KeyForPath(home, abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Put(context.Background(), key, strings.NewReader("stale-remote")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("fresh-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(context.Background(), abs); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, err := os.ReadFile(abs); err != nil || string(data) != "fresh-local" {
		t.Fatalf("data=%q err=%v, want the concurrent local write to win", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(abs))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stella-asset-") {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
}

func TestHydrateUserLocalWinsAndSingleFlight(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	local := filepath.Join(assetsDir, "keep.txt")
	remoteKey, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Put(context.Background(), remoteKey, strings.NewReader("remote")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("local-wins"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.HydrateUser(context.Background(), assetsDir); err != nil {
		t.Fatalf("HydrateUser: %v", err)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "local-wins" {
		t.Fatalf("data=%q err=%v, want existing local file preserved", data, err)
	}
	s.resetHydrationForTest()
	if err := s.HydrateUser(context.Background(), assetsDir); err != nil {
		t.Fatalf("HydrateUser after reset: %v", err)
	}
}

// TestHydrateSkipsTraversalKey re-adds the regression coverage lost with the old
// hydrate_test: a malicious authority listing that echoes a traversal key must be
// re-validated and skipped, never written outside home.
func TestHydrateSkipsTraversalKey(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	// Inject a traversal key the backend "echoes" under the listed prefix.
	auth.echoKey = "users/u1/data/assets/../../../../etc/evil"
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	// A legitimate key so the list is non-empty and hydration runs.
	goodKey, _ := blob.KeyForPath(home, filepath.Join(assetsDir, "ok.txt"))
	if err := auth.Put(context.Background(), goodKey, strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	if err := s.HydrateUser(context.Background(), assetsDir); err != nil {
		t.Fatalf("HydrateUser: %v", err)
	}
	// The traversal target must never be created.
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "etc", "evil")); !os.IsNotExist(err) {
		t.Fatalf("traversal key escaped home: %v", err)
	}
	// The legitimate key materialized.
	if _, err := os.Stat(filepath.Join(assetsDir, "ok.txt")); err != nil {
		t.Fatalf("legitimate key not hydrated: %v", err)
	}
}

func TestMoveFilePersistsDestinationBeforeDeletingSource(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(assetsDir, "a.txt")
	dst := filepath.Join(assetsDir, "b.txt")
	if err := os.WriteFile(src, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcKey, _ := blob.KeyForPath(home, src)
	dstKey, _ := blob.KeyForPath(home, dst)
	if err := auth.Put(context.Background(), srcKey, strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveFile(context.Background(), src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if auth.has(srcKey) {
		t.Fatal("source key still present after move")
	}
	if string(auth.objs[dstKey]) != "body" {
		t.Fatalf("dst key data=%q", auth.objs[dstKey])
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst local missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src local still present: %v", err)
	}
}

func TestMoveFileRollsBackOnAuthorityFailure(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(assetsDir, "a.txt")
	dst := filepath.Join(assetsDir, "b.txt")
	if err := os.WriteFile(src, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcKey, _ := blob.KeyForPath(home, src)
	if err := auth.Put(context.Background(), srcKey, strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	// Fail the destination Put.
	auth.failPutAfter = 0
	auth.puts = 0
	if err := s.MoveFile(context.Background(), src, dst); err == nil {
		t.Fatal("expected MoveFile to fail when the destination authority write fails")
	}
	// Source authority must remain intact and the local rename rolled back.
	if !auth.has(srcKey) {
		t.Fatal("source authority key deleted despite a failed move")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("local rename not rolled back, src missing: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination present after rolled-back move: %v", err)
	}
	// Retry after the authority recovers succeeds.
	auth.failPutAfter = -1
	if err := s.MoveFile(context.Background(), src, dst); err != nil {
		t.Fatalf("retry MoveFile: %v", err)
	}
}

func TestMoveFileSourceCleanupFailureRollsBackWholeMove(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(assetsDir, "src.txt")
	dst := filepath.Join(assetsDir, "dst.txt")
	if err := os.WriteFile(src, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcKey, _ := blob.KeyForPath(home, src)
	dstKey, _ := blob.KeyForPath(home, dst)
	if err := auth.Put(context.Background(), srcKey, strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	// Persist of the destination succeeds; the source-key delete fails.
	auth.failDeleteContains = "src.txt"
	err := s.MoveFile(context.Background(), src, dst)
	if err == nil {
		t.Fatal("expected MoveFile to fail when source authority cleanup fails")
	}
	// The whole move is rolled back to the original state: local content at src,
	// destination absent locally, source authority preserved, destination keys
	// removed. There is no retryable source once the rename stands, so this must
	// NOT report success or leave a durable half-move.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("local not rolled back, src missing: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination present after rolled-back move: %v", err)
	}
	if !auth.has(srcKey) {
		t.Fatal("source authority key not preserved after rolled-back move")
	}
	if auth.has(dstKey) {
		t.Fatal("destination authority key left behind after rolled-back move")
	}
	// After the authority recovers, the move retries cleanly from the restored src.
	auth.failDeleteContains = ""
	if err := s.MoveFile(context.Background(), src, dst); err != nil {
		t.Fatalf("retry MoveFile after recovery: %v", err)
	}
	if auth.has(srcKey) || !auth.has(dstKey) {
		t.Fatal("retry did not complete the move in the authority")
	}
}

func TestMoveDirectoryPersistsAllThenDeletesSourcePrefix(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	srcDir := filepath.Join(assetsDir, "old")
	dstDir := filepath.Join(assetsDir, "new")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(srcDir, "a.txt"):        "A",
		filepath.Join(srcDir, "sub", "b.txt"): "B",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		key, _ := blob.KeyForPath(home, p)
		if err := auth.Put(context.Background(), key, strings.NewReader(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MoveFile(context.Background(), srcDir, dstDir); err != nil {
		t.Fatalf("MoveFile dir: %v", err)
	}
	// Every source-prefix key gone; every destination key present.
	for p, c := range files {
		srcKey, _ := blob.KeyForPath(home, p)
		if auth.has(srcKey) {
			t.Fatalf("source key %q survived directory move", srcKey)
		}
		dstPath := strings.Replace(p, srcDir, dstDir, 1)
		dstKey, _ := blob.KeyForPath(home, dstPath)
		if string(auth.objs[dstKey]) != c {
			t.Fatalf("dst key %q data=%q want %q", dstKey, auth.objs[dstKey], c)
		}
	}
}

func TestMoveDirectoryPartialPutLeavesSourceIntact(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	srcDir := filepath.Join(assetsDir, "old")
	dstDir := filepath.Join(assetsDir, "new")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var srcKeys []string
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		p := filepath.Join(srcDir, n)
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		key, _ := blob.KeyForPath(home, p)
		if err := auth.Put(context.Background(), key, strings.NewReader(n)); err != nil {
			t.Fatal(err)
		}
		srcKeys = append(srcKeys, key)
	}
	// Fail after one destination Put succeeds (partial directory persist).
	auth.puts = 0
	auth.failPutAfter = 1
	if err := s.MoveFile(context.Background(), srcDir, dstDir); err == nil {
		t.Fatal("expected directory MoveFile to fail on a partial destination write")
	}
	// Source authority keys must all remain, and the local dir rolled back.
	for _, k := range srcKeys {
		if !auth.has(k) {
			t.Fatalf("source key %q lost on partial-failure move", k)
		}
	}
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("source dir not rolled back: %v", err)
	}
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Fatalf("destination dir present after rolled-back move: %v", err)
	}
	// No orphan destination keys were left in the authority.
	for k := range auth.objs {
		if strings.Contains(k, "/new/") {
			t.Fatalf("orphan destination key left after rollback: %q", k)
		}
	}
}

func TestRemoveFileDeletesFileKey(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	abs := filepath.Join(assetsDirFor(home, "u1"), "x.txt")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, _ := blob.KeyForPath(home, abs)
	if err := auth.Put(context.Background(), key, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFile(context.Background(), abs); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if auth.has(key) {
		t.Fatal("file key present after RemoveFile")
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("local file present after RemoveFile: %v", err)
	}
}

func TestRemoveDirectoryDeletesAllChildKeysSoHydrationCannotResurrect(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	s := mustStore(t, home, auth)
	assetsDir := assetsDirFor(home, "u1")
	dir := filepath.Join(assetsDir, "folder")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	children := []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "sub", "b.txt"),
	}
	for _, p := range children {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		key, _ := blob.KeyForPath(home, p)
		if err := auth.Put(context.Background(), key, strings.NewReader("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveFile(context.Background(), dir); err != nil {
		t.Fatalf("RemoveFile dir: %v", err)
	}
	if auth.count() != 0 {
		t.Fatalf("authority still holds %d child key(s) after directory delete", auth.count())
	}
	// Hydration on a fresh instance must not resurrect the deleted directory.
	home2 := t.TempDir()
	s2 := mustStore(t, home2, auth)
	if err := s2.HydrateUser(context.Background(), assetsDirFor(home2, "u1")); err != nil {
		t.Fatalf("HydrateUser: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home2, "users", "u1", "data", "assets", "folder")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory resurrected by hydration: %v", err)
	}
}

func TestRemoveFileAuthorityFailureLeavesLocalIntact(t *testing.T) {
	home := t.TempDir()
	auth := newMemAuthority()
	auth.failDelete = true
	s := mustStore(t, home, auth)
	abs := filepath.Join(assetsDirFor(home, "u1"), "x.txt")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, _ := blob.KeyForPath(home, abs)
	if err := auth.Put(context.Background(), key, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFile(context.Background(), abs); err == nil {
		t.Fatal("expected RemoveFile to fail when the authority delete fails")
	}
	// Authority delete failed before local removal: local file is retry-safe.
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("local file removed despite authority delete failure: %v", err)
	}
}
