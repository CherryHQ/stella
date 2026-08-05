package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type migrationSource struct {
	mu      sync.Mutex
	objects map[string][]byte
	lists   [][]string
	calls   int
	opens   map[string]int
	openFn  func(context.Context, string, int) (io.ReadCloser, error)
}

func (s *migrationSource) List(_ context.Context, prefix string) ([]string, error) {
	if prefix != "users" {
		return nil, errors.New("unexpected prefix")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.lists[len(s.lists)-1]
	if s.calls < len(s.lists) {
		keys = s.lists[s.calls]
	}
	s.calls++
	return append([]string(nil), keys...), nil
}

func (s *migrationSource) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	if s.opens == nil {
		s.opens = make(map[string]int)
	}
	s.opens[key]++
	count := s.opens[key]
	openFn := s.openFn
	data, ok := s.objects[key]
	data = append([]byte(nil), data...)
	s.mu.Unlock()
	if openFn != nil {
		return openFn(ctx, key, count)
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *migrationSource) openCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens[key]
}

type failingReadCloser struct {
	reader   io.Reader
	closeErr error
}

func (r failingReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r failingReadCloser) Close() error               { return r.closeErr }

type errorAfterReader struct {
	data []byte
	err  error
	done bool
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return 0, r.err
}

func seedMutableAssetUser(t *testing.T, r *Registry, id string) {
	t.Helper()
	if _, err := r.db.Exec(context.Background(), "INSERT INTO auth_user (id, email) VALUES ($1, $2)", id, id+"@example.test"); err != nil {
		t.Fatal(err)
	}
}

func seedMutableAssetGroup(t *testing.T, r *Registry, id string) {
	t.Helper()
	if _, err := r.db.Exec(context.Background(), "INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES ($1, 'test', $2)", id, "group-"+id); err != nil {
		t.Fatal(err)
	}
}

func TestMutableAssetMigrationDryRunDoesNotMutateRegistryOrMarker(t *testing.T) {
	r, store := newRegistry(t)
	userID := uuid.NewString()
	seedMutableAssetUser(t, r, userID)
	key := "users/" + userID + "/data/assets/notes/a.txt"
	source := &migrationSource{objects: map[string][]byte{key: []byte("hello")}, lists: [][]string{{key}}}

	summary, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.DryRun || summary.Status != "planned" || summary.Count != 1 || summary.Bytes != 5 || summary.SHA256 == "" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("dry run wrote marker: %v", err)
	}
	if _, err := r.get(context.Background(), Principal(UserPrincipal, userID)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("dry run wrote Home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.base, "users", userID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote Home bytes: %v", err)
	}
}

func TestMutableAssetMigrationDryRunSynthesizesPendingFromHistoricalNotRequired(t *testing.T) {
	r, _ := newRegistry(t)
	id := uuid.NewString()
	seedMutableAssetUser(t, r, id)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	before, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil {
		t.Fatal(err)
	}
	key := "users/" + id + "/data/assets/a.txt"
	summary, err := r.MigrateMutableAssets(ctx, &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}}, MutableAssetMigrationOptions{DryRun: true})
	if err != nil || summary.MarkerState != "pending" || !summary.DryRun {
		t.Fatalf("dry-run summary = %#v, %v", summary, err)
	}
	after, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || after.State != "not_required" || after.ObjectAuthorityConfigured || string(after.Metadata) != string(before.Metadata) || after.CompletedAt != before.CompletedAt {
		t.Fatalf("durable marker changed: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestMutableAssetMigrationCopiesTypedUserAndGroupHomesIdempotently(t *testing.T) {
	r, store := newRegistry(t)
	id := uuid.NewString()
	seedMutableAssetUser(t, r, id)
	seedMutableAssetGroup(t, r, id)
	userKey := "users/" + id + "/data/assets/u.txt"
	groupKey := "users/group-" + id + "/data/assets/nested/g.txt"
	source := &migrationSource{objects: map[string][]byte{userKey: []byte("user"), groupKey: []byte("group")}, lists: [][]string{{groupKey, userKey}}}

	summary, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" || summary.Count != 2 || summary.Bytes != 9 {
		t.Fatalf("summary = %#v", summary)
	}
	for path, want := range map[string]string{
		filepath.Join(store.base, "users", id, "data", "assets", "u.txt"):                    "user",
		filepath.Join(store.base, "users", "group-"+id, "data", "assets", "nested", "g.txt"): "group",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", path, got, err)
		}
	}
	if len(source.objects) != 2 {
		t.Fatalf("migration changed remote source: %#v", source.objects)
	}
	again, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
	if err != nil || again != summary {
		t.Fatalf("idempotent rerun = %#v, %v; want %#v", again, err, summary)
	}
}

func TestMutableAssetMigrationRejectsMalformedUnknownAndDuplicateKeys(t *testing.T) {
	r, _ := newRegistry(t)
	userID := uuid.NewString()
	seedMutableAssetUser(t, r, userID)
	for _, keys := range [][]string{
		{"users/unknown/data/assets/a.txt"},
		{"users/" + userID + "/data/assets/../a.txt"},
		{"users/" + userID + "/data/assets/a.txt", "users/" + userID + "/data/assets/a.txt"},
		{"users/" + userID + "/data/assets"},
	} {
		source := &migrationSource{objects: map[string][]byte{}, lists: [][]string{keys}}
		if _, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{DryRun: true}); err == nil {
			t.Fatalf("keys %q accepted", keys)
		}
	}
}

func TestMutableAssetMigrationIgnoresSeparateObjectFamilies(t *testing.T) {
	r, _ := newRegistry(t)
	userID := uuid.NewString()
	seedMutableAssetUser(t, r, userID)
	keys := []string{
		"users/" + userID + "/session-media/abc",
		"users/" + userID + "/data/cache/a",
	}
	summary, err := r.MigrateMutableAssets(context.Background(), &migrationSource{objects: map[string][]byte{}, lists: [][]string{keys}}, MutableAssetMigrationOptions{DryRun: true})
	if err != nil || summary.Count != 0 || summary.Bytes != 0 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
}

func TestMutableAssetMigrationOwnerTokenCollisionFailsClosed(t *testing.T) {
	groupID := uuid.NewString()
	if _, err := mutableAssetOwnerMap([]string{"group-" + groupID}, []string{groupID}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestMutableAssetObservationTransitionsAndStartupGate(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, false); err != nil {
		t.Fatalf("not_required gate: %v", err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err != nil {
		t.Fatalf("not_required -> pending: %v", err)
	}
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || marker.State != "pending" || !marker.ObjectAuthorityConfigured {
		t.Fatalf("pending marker = %#v, %v", marker, err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, true); err == nil {
		t.Fatal("pending marker allowed startup")
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err == nil {
		t.Fatal("pending marker accepted removed object authority")
	}
	if _, err := r.q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: MutableAssetObjectAuthorityMigration, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	count, size, digest := aggregateMutableAssets(nil)
	metadata, err := json.Marshal(mutableAssetMetadata{Layout: mutableAssetLayout, SourceCount: count, SourceBytes: size, SourceSHA256: digest, TargetCount: count, TargetBytes: size, TargetSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_migration SET metadata = $2 WHERE name = $1", MutableAssetObjectAuthorityMigration, metadata); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatalf("completed marker regressed: %v", err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, false); err != nil {
		t.Fatalf("completed historical authority blocked startup: %v", err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_migration SET state = 'bad' WHERE name = $1", MutableAssetObjectAuthorityMigration); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, false); err == nil {
		t.Fatal("malformed marker allowed startup")
	}
}

func TestMutableAssetMigrationFailsClosedForExistingDifferenceAndSourceChange(t *testing.T) {
	t.Run("existing target differs", func(t *testing.T) {
		r, store := newRegistry(t)
		userID := uuid.NewString()
		seedMutableAssetUser(t, r, userID)
		home, err := r.Ensure(context.Background(), Principal(UserPrincipal, userID))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.base, filepath.FromSlash(home.Locator), "data", "assets", "a.txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("local"), 0o644); err != nil {
			t.Fatal(err)
		}
		key := "users/" + userID + "/data/assets/a.txt"
		if _, err := r.MigrateMutableAssets(context.Background(), &migrationSource{objects: map[string][]byte{key: []byte("remote")}, lists: [][]string{{key}}}, MutableAssetMigrationOptions{}); err == nil || strings.Contains(err.Error(), "outcome unknown") {
			t.Fatalf("difference error = %v", err)
		}
	})
	t.Run("source changes on final relist", func(t *testing.T) {
		r, _ := newRegistry(t)
		userID := uuid.NewString()
		seedMutableAssetUser(t, r, userID)
		first := "users/" + userID + "/data/assets/a.txt"
		second := "users/" + userID + "/data/assets/b.txt"
		source := &migrationSource{objects: map[string][]byte{first: []byte("a"), second: []byte("b")}, lists: [][]string{{first}, {first, second}}}
		_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			t.Fatalf("source change error = %v, want outcome unknown", err)
		}
	})
}

func TestMutableAssetMigrationSourceFailuresLeavePendingAndDoNotRetry(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reader func(context.CancelFunc) io.ReadCloser
	}{
		{name: "lazy read", reader: func(_ context.CancelFunc) io.ReadCloser {
			return failingReadCloser{reader: &errorAfterReader{err: errors.New("lazy read failure")}}
		}},
		{name: "close", reader: func(_ context.CancelFunc) io.ReadCloser {
			return failingReadCloser{reader: bytes.NewReader([]byte("bytes")), closeErr: errors.New("close failure")}
		}},
		{name: "cancellation", reader: func(cancel context.CancelFunc) io.ReadCloser {
			cancel()
			return io.NopCloser(bytes.NewReader([]byte("bytes")))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, store := newRegistry(t)
			id := uuid.NewString()
			seedMutableAssetUser(t, r, id)
			key := "users/" + id + "/data/assets/a.txt"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}, openFn: func(_ context.Context, _ string, _ int) (io.ReadCloser, error) {
				return tt.reader(cancel), nil
			}}
			if _, err := r.MigrateMutableAssets(ctx, source, MutableAssetMigrationOptions{}); err == nil {
				t.Fatal("source failure completed migration")
			}
			marker, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
			if err != nil || marker.State != "pending" || marker.CompletedAt.Valid {
				t.Fatalf("marker = %#v, %v", marker, err)
			}
			if source.openCount(key) != 1 {
				t.Fatalf("Open(%q) = %d, want one attempt", key, source.openCount(key))
			}
			assertNoMutableAssetTemps(t, store.base)
		})
	}

	t.Run("failure after publication is outcome unknown", func(t *testing.T) {
		r, store := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		first := "users/" + id + "/data/assets/a.txt"
		second := "users/" + id + "/data/assets/b.txt"
		source := &migrationSource{objects: map[string][]byte{first: []byte("a"), second: []byte("b")}, lists: [][]string{{first, second}}, openFn: func(_ context.Context, key string, _ int) (io.ReadCloser, error) {
			if key == second {
				return failingReadCloser{reader: &errorAfterReader{err: errors.New("second object failed")}}, nil
			}
			return io.NopCloser(bytes.NewReader([]byte("a"))), nil
		}}
		_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			t.Fatalf("error = %v, want outcome unknown", err)
		}
		if source.openCount(first) != 1 || source.openCount(second) != 1 {
			t.Fatalf("Open counts first=%d second=%d", source.openCount(first), source.openCount(second))
		}
		marker, markerErr := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
		if markerErr != nil || marker.State != "pending" || marker.CompletedAt.Valid {
			t.Fatalf("marker = %#v, %v", marker, markerErr)
		}
		assertNoMutableAssetTemps(t, store.base)
	})
}

func TestMutableAssetMigrationFinalVerificationDetectsChangedContentAndOwnerInventory(t *testing.T) {
	t.Run("same key content changes", func(t *testing.T) {
		r, _ := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/a.txt"
		source := &migrationSource{objects: map[string][]byte{key: []byte("before")}, lists: [][]string{{key}}, openFn: func(_ context.Context, _ string, count int) (io.ReadCloser, error) {
			if count == 1 {
				return io.NopCloser(bytes.NewReader([]byte("before"))), nil
			}
			return io.NopCloser(bytes.NewReader([]byte("after"))), nil
		}}
		_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			t.Fatalf("changed content error = %v", err)
		}
		if source.openCount(key) != 2 {
			t.Fatalf("Open(%q) = %d, want install plus one final verification", key, source.openCount(key))
		}
	})
	t.Run("authoritative owner inventory changes", func(t *testing.T) {
		r, _ := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/a.txt"
		var once sync.Once
		source := &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}, openFn: func(_ context.Context, _ string, _ int) (io.ReadCloser, error) {
			once.Do(func() {
				if _, err := r.db.Exec(context.Background(), "DELETE FROM auth_user WHERE id = $1", id); err != nil {
					t.Errorf("remove source owner: %v", err)
				}
			})
			return io.NopCloser(bytes.NewReader([]byte("bytes"))), nil
		}}
		_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			t.Fatalf("changed owner inventory error = %v", err)
		}
	})
}

func TestLocalStoreMutableAssetInstallDestinationSafetyAndCleanup(t *testing.T) {
	r, store := newRegistry(t)
	id := uuid.NewString()
	seedMutableAssetUser(t, r, id)
	home, err := r.Ensure(context.Background(), Principal(UserPrincipal, id))
	if err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(store.base, filepath.FromSlash(home.Locator), "data", "assets", "a.txt")
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath, []byte("equal"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, published, err := store.installMutableAsset(context.Background(), home, "a.txt", io.NopCloser(bytes.NewReader([]byte("equal")))); err != nil || published {
		t.Fatalf("existing equal install published=%v err=%v", published, err)
	}
	if err := os.Remove(assetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(assetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.installMutableAsset(context.Background(), home, "a.txt", io.NopCloser(bytes.NewReader([]byte("bytes")))); err == nil {
		t.Fatal("non-regular destination accepted")
	}
	if err := os.Remove(assetPath); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, body := range [][]byte{[]byte("one"), []byte("two")} {
		go func() {
			<-start
			_, _, err := store.installMutableAsset(context.Background(), home, "a.txt", io.NopCloser(bytes.NewReader(body)))
			results <- err
		}()
	}
	close(start)
	var successes, failures int
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent installs success=%d failures=%d", successes, failures)
	}
	got, err := os.ReadFile(assetPath)
	if err != nil || (string(got) != "one" && string(got) != "two") {
		t.Fatalf("winner = %q, %v", got, err)
	}
	assertNoMutableAssetTemps(t, store.base)
}

func TestMutableAssetMigrationRequiresFilesystemDurabilityBeforeCompletion(t *testing.T) {
	newFixture := func(t *testing.T) (*Registry, *LocalStore, *migrationSource, string) {
		t.Helper()
		r, store := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/a.txt"
		return r, store, &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}}, key
	}
	markerMustRemainPending := func(t *testing.T, r *Registry) {
		t.Helper()
		marker, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
		if err != nil || marker.State != "pending" || marker.CompletedAt.Valid {
			t.Fatalf("marker = %#v, %v", marker, err)
		}
	}

	t.Run("temp sync failure publishes nothing", func(t *testing.T) {
		r, store, source, key := newFixture(t)
		store.syncFile = func(file *os.File) error {
			if strings.HasPrefix(filepath.Base(file.Name()), ".stella-migrate-") {
				return errors.New("injected temp sync failure")
			}
			return nil
		}
		if _, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{}); err == nil {
			t.Fatal("temp sync failure completed migration")
		}
		if _, err := os.Stat(filepath.Join(store.base, filepath.FromSlash(key))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("published target after temp sync failure: %v", err)
		}
		markerMustRemainPending(t, r)
		assertNoMutableAssetTemps(t, store.base)
	})

	t.Run("directory sync failure after publication is outcome unknown", func(t *testing.T) {
		r, store, source, key := newFixture(t)
		store.syncFile = func(file *os.File) error {
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if info.IsDir() {
				return errors.New("injected directory sync failure")
			}
			return nil
		}
		_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			t.Fatalf("directory sync error = %v, want outcome unknown", err)
		}
		if _, err := os.Stat(filepath.Join(store.base, filepath.FromSlash(key))); err != nil {
			t.Fatalf("target was not published before directory sync failure: %v", err)
		}
		markerMustRemainPending(t, r)
		assertNoMutableAssetTemps(t, store.base)
	})

	t.Run("successful migration syncs and completes", func(t *testing.T) {
		r, store, source, _ := newFixture(t)
		var syncs int
		store.syncFile = func(file *os.File) error {
			syncs++
			return nil
		}
		if _, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{}); err != nil {
			t.Fatal(err)
		}
		if syncs < 3 { // temp, verified inode, and at least one verified directory.
			t.Fatalf("sync calls = %d, want durable file and directory syncs", syncs)
		}
		marker, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
		if err != nil || marker.State != "completed" || !marker.CompletedAt.Valid {
			t.Fatalf("marker = %#v, %v", marker, err)
		}
	})
}

func TestMutableAssetMigrationEmptyAndTargetOnlyFilesAreIdempotent(t *testing.T) {
	t.Run("empty source", func(t *testing.T) {
		r, _ := newRegistry(t)
		source := &migrationSource{objects: map[string][]byte{}, lists: [][]string{{}}}
		first, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if err != nil || first.Count != 0 || first.Bytes != 0 || first.Status != "completed" {
			t.Fatalf("empty migration = %#v, %v", first, err)
		}
		marker, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
		if err != nil || !validMutableAssetMetadata(marker.Metadata) {
			t.Fatalf("empty marker = %#v, %v", marker, err)
		}
		second, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if err != nil || second != first {
			t.Fatalf("empty rerun = %#v, %v", second, err)
		}
	})
	t.Run("target-only file is preserved and excluded", func(t *testing.T) {
		r, store := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/source.txt"
		source := &migrationSource{objects: map[string][]byte{key: []byte("source")}, lists: [][]string{{key}}}
		first, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		extra := filepath.Join(store.base, "users", id, "data", "assets", "local-only.txt")
		if err := os.WriteFile(extra, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		second, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if err != nil || second != first {
			t.Fatalf("rerun = %#v, %v; want %#v", second, err, first)
		}
		if got, err := os.ReadFile(extra); err != nil || string(got) != "keep me" {
			t.Fatalf("target-only file = %q, %v", got, err)
		}
	})
}

func TestMutableAssetMigrationDryRunExistingAndStrictKeyGrammar(t *testing.T) {
	r, store := newRegistry(t)
	id := uuid.NewString()
	seedMutableAssetUser(t, r, id)
	key := "users/" + id + "/data/assets/a.txt"
	home, err := r.Ensure(context.Background(), Principal(UserPrincipal, id))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.base, filepath.FromSlash(home.Locator), "data", "assets", "a.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("equal"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var syncs int
	store.syncFile = func(*os.File) error {
		syncs++
		return errors.New("dry-run must not sync")
	}
	if _, err := r.MigrateMutableAssets(context.Background(), &migrationSource{objects: map[string][]byte{key: []byte("equal")}, lists: [][]string{{key}}}, MutableAssetMigrationOptions{DryRun: true}); err != nil {
		t.Fatalf("equal dry run: %v", err)
	}
	if syncs != 0 {
		t.Fatalf("dry run invoked durability sync %d time(s)", syncs)
	}
	if _, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("dry run wrote marker: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("dry run changed target: %v", err)
	}
	if _, err := r.MigrateMutableAssets(context.Background(), &migrationSource{objects: map[string][]byte{key: []byte("different")}, lists: [][]string{{key}}}, MutableAssetMigrationOptions{DryRun: true}); err == nil {
		t.Fatal("differing dry-run target accepted")
	}
	for _, malformed := range []string{
		"users/" + id + "//data/assets/a.txt",
		"users/" + id + "/data/./assets/a.txt",
		"users/" + id + "/data/assets/a\\b.txt",
		"users/" + id + "/misplaced/assets/a.txt",
	} {
		if _, err := r.MigrateMutableAssets(context.Background(), &migrationSource{objects: map[string][]byte{}, lists: [][]string{{malformed}}}, MutableAssetMigrationOptions{DryRun: true}); err == nil {
			t.Fatalf("malformed key accepted: %q", malformed)
		}
	}
}

func TestMutableAssetMigrationMarkerMetadataAndConcurrentCompletion(t *testing.T) {
	t.Run("completed exact metadata reruns and differing metadata fails", func(t *testing.T) {
		r, _ := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/a.txt"
		source := &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}}
		first, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if again, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{}); err != nil || again != first {
			t.Fatalf("exact completed rerun = %#v, %v", again, err)
		}
		if _, err := r.db.Exec(context.Background(), "UPDATE storage_migration SET metadata = '{\"layout\":\"different\"}' WHERE name = $1", MutableAssetObjectAuthorityMigration); err != nil {
			t.Fatal(err)
		}
		if _, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{}); err == nil {
			t.Fatal("completed marker with differing metadata accepted")
		}
	})
	t.Run("concurrent migrations converge", func(t *testing.T) {
		r, _ := newRegistry(t)
		id := uuid.NewString()
		seedMutableAssetUser(t, r, id)
		key := "users/" + id + "/data/assets/a.txt"
		source := &migrationSource{objects: map[string][]byte{key: []byte("bytes")}, lists: [][]string{{key}}}
		start := make(chan struct{})
		results := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				_, err := r.MigrateMutableAssets(context.Background(), source, MutableAssetMigrationOptions{})
				results <- err
			}()
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("concurrent migration: %v", err)
			}
		}
	})
}

func TestMutableAssetMigrationGateMatrixPreservesPendingAndValidatesCompleted(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_migration SET metadata = '{\"preserve\":true}' WHERE name = $1", MutableAssetObjectAuthorityMigration); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err == nil {
		t.Fatal("pending marker downgraded after authority removal")
	}
	pending, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	var preserved map[string]bool
	_ = json.Unmarshal(pending.Metadata, &preserved)
	if err != nil || pending.State != "pending" || !pending.ObjectAuthorityConfigured || !preserved["preserve"] {
		t.Fatalf("pending marker = %#v, %v", pending, err)
	}
	if _, err := r.q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: MutableAssetObjectAuthorityMigration, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_migration SET metadata = '{}' WHERE name = $1", MutableAssetObjectAuthorityMigration); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, true); err == nil {
		t.Fatal("malformed completed configured marker allowed startup")
	}
	count, size, digest := aggregateMutableAssets(nil)
	valid, err := json.Marshal(mutableAssetMetadata{Layout: mutableAssetLayout, SourceCount: count, SourceBytes: size, SourceSHA256: digest, TargetCount: count, TargetBytes: size, TargetSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_migration SET metadata = $2 WHERE name = $1", MutableAssetObjectAuthorityMigration, valid); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, true); err != nil {
		t.Fatalf("valid completed configured marker blocked startup: %v", err)
	}
	if err := r.ValidateMutableAssetMigrationGate(ctx, false); err != nil {
		t.Fatalf("valid completed historical marker blocked startup: %v", err)
	}
}

func assertNoMutableAssetTemps(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".stella-migrate-") {
			t.Fatalf("temporary migration artifact remains: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
