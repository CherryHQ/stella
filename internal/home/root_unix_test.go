//go:build unix

package home

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func testSkillOperationsRoot(t *testing.T, access RootAccess) *Root {
	t.Helper()
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := &Root{root: r, access: access, unlock: func() {}}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestRootSkillPublicationPrimitives(t *testing.T) {
	r := testSkillOperationsRoot(t, RootReadWrite)
	if err := r.Mkdir(t.Context(), "revisions/one", 0o755, MkdirOptions{Parents: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncDirectory(t.Context(), "revisions"); err != nil {
		t.Fatal(err)
	}
	if err := r.Symlink(t.Context(), "revisions/one", "current.next"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Readlink(t.Context(), "current.next"); err != nil || got != "revisions/one" {
		t.Fatalf("Readlink = %q, %v", got, err)
	}
	if err := r.Rename(t.Context(), "current.next", "current", RenameOptions{SyncParent: true}); err != nil {
		t.Fatal(err)
	}
	if info, err := r.Lstat(t.Context(), "current"); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("selector = %v, %v", info, err)
	}
	for _, target := range []string{"/tmp/out", "../out", `revisions\out`, "revisions/../out"} {
		if err := r.Symlink(t.Context(), target, "bad"); err == nil {
			t.Fatalf("unsafe target %q accepted", target)
		}
	}
}

func TestRootRenameNoReplaceRace(t *testing.T) {
	r := testSkillOperationsRoot(t, RootReadWrite)
	for _, name := range []string{"one", "two"} {
		if err := r.Write(t.Context(), name, strings.NewReader(name), WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			<-start
			errs <- r.Rename(t.Context(), name, "winner", RenameOptions{NoReplace: true})
		}()
	}
	close(start)
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("rename errors = %v, %v; want one winner", first, second)
	}
	loser := first
	if loser == nil {
		loser = second
	}
	if !errors.Is(loser, fs.ErrExist) || IsOutcomeUnknown(loser) {
		t.Fatalf("loser = %v, want known fs.ErrExist", loser)
	}
}

func TestSkillRootAncestryFenceFailureIsRetried(t *testing.T) {
	db, base := dbtest.New(t), t.TempDir()
	user := uuid.NewString()
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	original := fsyncWorkspaceFD
	t.Cleanup(func() { fsyncWorkspaceFD = original })
	calls := 0
	fsyncWorkspaceFD = func(fd int) error {
		calls++
		if calls == 3 {
			return errors.New("injected fsync failure")
		}
		return original(fd)
	}
	if _, err := m.OpenRoot(t.Context(), WorkspaceRequest{UserID: user}, RootUserSkills, RootReadWrite); err == nil {
		t.Fatal("OpenRoot passed failed ancestry fence")
	}
	firstCalls := calls
	fsyncWorkspaceFD = original
	r, err := m.OpenRoot(t.Context(), WorkspaceRequest{UserID: user}, RootUserSkills, RootReadWrite)
	if err != nil {
		t.Fatalf("resume did not re-fence visible ancestry: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if firstCalls < 3 {
		t.Fatalf("first attempt fsync calls = %d", firstCalls)
	}
}

func TestRootRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	db, ctx, base := dbtest.New(t), context.Background(), t.TempDir()
	user, agentID := uuid.NewString(), "special-agent"
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Agent', '')`, agentID); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	r, err := m.OpenRoot(ctx, WorkspaceRequest{UserID: user, AgentID: agentID}, RootAgentWorkspace, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	fifo := filepath.Join(base, "users", user, "agents", agentID, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Read(ctx, "fifo", &bytes.Buffer{}, ReadOptions{MaxBytes: 1}); err == nil {
		t.Fatal("FIFO read succeeded")
	}
	if err := r.Write(ctx, "fifo", strings.NewReader("x"), WriteOptions{}); err == nil {
		t.Fatal("FIFO write succeeded")
	}
	if info, err := os.Lstat(fifo); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO changed: %v, %v", info, err)
	}
}
