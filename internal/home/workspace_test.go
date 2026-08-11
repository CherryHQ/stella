package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
