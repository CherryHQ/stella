//go:build unix

package home

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

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
