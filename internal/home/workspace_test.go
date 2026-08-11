package home

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type commitThenErrorBlobStore struct{ blob.Store }

func (s commitThenErrorBlobStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.Store.Put(ctx, key, r); err != nil {
		return err
	}
	return errors.New("blob Put committed then returned an error")
}

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestWorkspaceManagerMaterializesDeterministicTypedLayout(t *testing.T) {
	db, ctx, base := dbtest.New(t), context.Background(), t.TempDir()
	user := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('a', 'Agent', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: user, Platform: "test", PlatformGroupID: user, GroupName: "group"}); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	u, err := m.WorkspaceView(ctx, WorkspaceRequest{UserID: user, AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.WorkspaceView(ctx, WorkspaceRequest{UserID: user, GroupID: user, AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if u.PrincipalRoot == g.PrincipalRoot || u.PrincipalRoot != filepath.Join(base, "users", user) || g.PrincipalRoot != filepath.Join(base, "users", "group-"+user) {
		t.Fatalf("typed paths: %#v %#v", u, g)
	}
	if _, err := m.WorkspaceView(ctx, WorkspaceRequest{UserID: user, AgentID: "a"}); err != nil {
		t.Fatalf("idempotent view: %v", err)
	}
	for _, p := range []string{filepath.Join(u.AgentRoot, ".agents", "skills"), filepath.Join(base, "agents", "a", ".agents", "skills"), u.DataRoot} {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Fatalf("missing scaffold %s: %v", p, err)
		}
	}
}

func TestWorkspaceManagerRejectsUnsafeTypedRoots(t *testing.T) {
	db, ctx, base := dbtest.New(t), context.Background(), t.TempDir()
	user := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	m, _ := NewWorkspaceManager(db, base)
	t.Cleanup(func() { _ = m.Close() })
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('a', 'Agent', '')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../x", "a/b", `.`} {
		if _, err := m.WorkspaceView(ctx, WorkspaceRequest{UserID: user, AgentID: id}); err == nil {
			t.Fatalf("unsafe ID %q accepted", id)
		}
	}
	if err := os.Mkdir(filepath.Join(base, "users"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "users", user)); err != nil {
		t.Skip(err)
	}
	if _, err := m.WorkspaceView(ctx, WorkspaceRequest{UserID: user, AgentID: "a"}); err == nil {
		t.Fatal("symlink typed root accepted")
	}
}

func TestWorkspaceManagerResolvesCompatibilityCoordinates(t *testing.T) {
	base := t.TempDir()
	m := &WorkspaceManager{base: base}
	req := WorkspaceRequest{UserID: "u", AgentID: "a"}
	agentRoot := filepath.Join(base, "users", "u", "agents", "a")
	dataRoot := filepath.Join(base, "users", "u", "data")
	if err := os.MkdirAll(filepath.Join(agentRoot, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentRoot, "dir", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value           string
		selected, scope RootScope
		name            string
	}{
		{"plain/file", RootPrincipalData, RootPrincipalData, "plain/file"},
		{"/workspace/dir/file", RootPrincipalData, RootAgentWorkspace, "dir/file"},
		{"/user/assets", RootAgentWorkspace, RootPrincipalData, "assets"},
		{"$HOME/dir/file", RootPrincipalData, RootAgentWorkspace, "dir/file"},
		{"$STELLA_ASSETS_DIR/x", RootAgentWorkspace, RootPrincipalData, "assets/x"},
		{filepath.Join(agentRoot, "dir", "file"), RootPrincipalData, RootAgentWorkspace, "dir/file"},
		{filepath.Join(agentRoot, "dir", "historical", "deleted.txt"), RootPrincipalData, RootAgentWorkspace, "dir/historical/deleted.txt"},
	} {
		scope, name, err := m.ResolveCoordinate(Coordinate{Request: req, Scope: tc.selected, Value: tc.value})
		if err != nil || scope != tc.scope || name != tc.name {
			t.Fatalf("resolve %q = %v %q %v", tc.value, scope, name, err)
		}
	}
	for _, value := range []string{"../escape", "$HOMELESS/x", filepath.Join(base, "outside")} {
		if _, _, err := m.ResolveCoordinate(Coordinate{Request: req, Scope: RootAgentWorkspace, Value: value}); err == nil {
			t.Fatalf("escape %q accepted", value)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(agentRoot, "escape")); err == nil {
		if _, _, err := m.ResolveCoordinate(Coordinate{Request: req, Scope: RootAgentWorkspace, Value: filepath.Join(agentRoot, "escape", "missing.txt")}); err == nil {
			t.Fatal("escaping symlink accepted")
		}
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(agentRoot, "dangling-escape")); err == nil {
		if _, _, err := m.ResolveCoordinate(Coordinate{Request: req, Scope: RootAgentWorkspace, Value: filepath.Join(agentRoot, "dangling-escape", "file.txt")}); err == nil {
			t.Fatal("dangling escaping symlink accepted")
		}
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "var")
	if err := os.Symlink(base, alias); err == nil {
		scope, name, err := m.ResolveCoordinate(Coordinate{Request: req, Scope: RootPrincipalData, Value: filepath.Join(alias, "users", "u", "agents", "a", "dir", "file")})
		if err != nil || scope != RootAgentWorkspace || name != "dir/file" {
			t.Fatalf("symlink alias = %v %q %v", scope, name, err)
		}
	}
}

func TestResolveLogicalCoordinateAllowsDotOnlyForRoot(t *testing.T) {
	if _, _, err := ResolveLogicalCoordinate(RootAgentWorkspace, ".", false); err == nil {
		t.Fatal("dot coordinate accepted without AllowRoot")
	}
	scope, name, err := ResolveLogicalCoordinate(RootAgentWorkspace, ".", true)
	if err != nil || scope != RootAgentWorkspace || name != "." {
		t.Fatalf("AllowRoot dot = %v %q %v", scope, name, err)
	}
}

func TestAssetCompatibilityRestoresObjectOnlyWorkspaceAssetWhileRootIsOpen(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	user := uuid.NewString()
	agentID := "asset-restore-agent"
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Agent', '')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(base, remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkspaceRequest{UserID: user, AgentID: agentID}
	target := filepath.Join(base, "users", user, "data", "assets", "legacy.txt")
	key, err := blob.KeyForPath(base, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(ctx, key, strings.NewReader("legacy object")); err != nil {
		t.Fatal(err)
	}
	root, err := m.OpenRoot(ctx, req, RootPrincipalData, RootReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreAsset(ctx, assets, Coordinate{Request: req, Scope: RootPrincipalData, Value: "assets/legacy.txt"}); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	var restored strings.Builder
	if err := root.Read(ctx, "assets/legacy.txt", &restored, ReadOptions{MaxBytes: 1024}); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if restored.String() != "legacy object" {
		t.Fatalf("restored = %q", restored.String())
	}
	rw, err := m.OpenRoot(ctx, req, RootPrincipalData, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	upload := Coordinate{Request: req, Scope: RootPrincipalData, Value: "assets/uploaded.txt"}
	if err := m.UploadAsset(ctx, assets, upload, strings.NewReader("exact"), WriteOptions{Mode: 0o600, MaxBytes: 5, Sync: true}); err != nil {
		_ = rw.Close()
		t.Fatalf("UploadAsset: %v", err)
	}
	if err := m.UploadAsset(ctx, assets, Coordinate{Request: req, Scope: RootPrincipalData, Value: "assets/too-large.txt"}, strings.NewReader("excess"), WriteOptions{MaxBytes: 5}); !errors.Is(err, ErrUploadLimit) {
		_ = rw.Close()
		t.Fatalf("over-limit UploadAsset error=%v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	uploadedKey, err := blob.KeyForPath(base, filepath.Join(base, "users", user, "data", "assets", "uploaded.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := remote.Open(ctx, uploadedKey)
	if err != nil {
		t.Fatalf("open mirrored upload: %v", err)
	}
	mirrored, readErr := io.ReadAll(rc)
	if err := errors.Join(readErr, rc.Close()); err != nil {
		t.Fatal(err)
	}
	if string(mirrored) != "exact" {
		t.Fatalf("mirrored upload=%q", mirrored)
	}
	if _, err := os.Stat(filepath.Join(base, "users", user, "data", "assets", "too-large.txt")); !os.IsNotExist(err) {
		t.Fatalf("over-limit compatibility upload published: %v", err)
	}
	commitAuthority, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unknownTarget := filepath.Join(base, "users", user, "data", "assets", "unknown.txt")
	unknownKey, err := blob.KeyForPath(base, unknownTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitAuthority.Put(ctx, unknownKey, strings.NewReader("object-only prior")); err != nil {
		t.Fatal(err)
	}
	unknownAssets, err := asset.NewStore(base, commitThenErrorBlobStore{Store: commitAuthority}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unknownRoot, err := m.OpenRoot(ctx, req, RootPrincipalData, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	err = m.WriteAsset(ctx, unknownAssets, Coordinate{Request: req, Scope: RootPrincipalData, Value: "assets/unknown.txt"}, []byte("possibly committed"), 0o600, false)
	closeErr := unknownRoot.Close()
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("WriteAsset error=%v, want ErrOutcomeUnknown", err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestAgentIDOccupancyTreatsEveryEntryAsReserved(t *testing.T) {
	m, _ := NewWorkspaceManager(dbtest.New(t), t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	if err := os.Mkdir(filepath.Join(m.base, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir", "file", "link"} {
		p := filepath.Join(m.base, "agents", name)
		switch name {
		case "dir":
			_ = os.Mkdir(p, 0o755)
		case "file":
			_ = os.WriteFile(p, []byte("x"), 0o600)
		case "link":
			_ = os.Symlink(t.TempDir(), p)
		}
		occupied, err := m.AgentIDOccupied(context.Background(), name)
		if err != nil || !occupied {
			t.Fatalf("%s occupied=%v err=%v", name, occupied, err)
		}
	}
}

func lockTestManager() *WorkspaceManager {
	m := &WorkspaceManager{}
	for i := range m.ownerLocks {
		m.ownerLocks[i] = make(chan struct{}, 1)
		m.ownerLocks[i] <- struct{}{}
	}
	return m
}

func lockShard(key string) int {
	h := uint32(2166136261)
	for i := range len(key) {
		h = (h ^ uint32(key[i])) * 16777619
	}
	return int(h) % 257
}

func TestWorkspaceManagerLockCollisionAndReverseOrderDoNotDeadlock(t *testing.T) {
	m := lockTestManager()
	first := "collision-0"
	second := ""
	for i := 1; second == ""; i++ {
		candidate := "collision-" + strconv.Itoa(i)
		if lockShard(candidate) == lockShard(first) {
			second = candidate
		}
	}
	unlock, err := m.lock(t.Context(), []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	unlock() // A same-shard collision must be acquired and released only once.

	done := make(chan error, 2)
	for _, keys := range [][]string{{"user:a", "agent:b"}, {"agent:b", "user:a"}} {
		go func(keys []string) {
			release, err := m.lock(t.Context(), keys)
			if err == nil {
				time.Sleep(time.Millisecond)
				release()
			}
			done <- err
		}(keys)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("reverse-order lock requests deadlocked")
		}
	}
}

func TestWorkspaceManagerLockCancellationReleasesPartialLocks(t *testing.T) {
	m := lockTestManager()
	keys := []string{"partial-a", "partial-b"}
	if lockShard(keys[0]) > lockShard(keys[1]) {
		keys[0], keys[1] = keys[1], keys[0]
	}
	blocked := lockShard(keys[1])
	<-m.ownerLocks[blocked]
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.lock(ctx, keys); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error = %v", err)
	}
	m.ownerLocks[blocked] <- struct{}{}
	release, err := m.lock(t.Context(), keys)
	if err != nil {
		t.Fatalf("partial lock leaked after cancellation: %v", err)
	}
	release()
}

func TestWorkspaceManagerMissingDurableOwnerLeavesNoFilesystemMutation(t *testing.T) {
	base := t.TempDir()
	db := dbtest.New(t)
	if _, err := db.Exec(t.Context(), `INSERT INTO agent (id, name, workspace) VALUES ('a', 'Agent', '')`); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.WorkspaceView(t.Context(), WorkspaceRequest{UserID: uuid.NewString(), AgentID: "a"}); err == nil {
		t.Fatal("missing owner accepted")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("durable-owner failure mutated filesystem: %v", entries)
	}
}
