package home

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestRootOperationsAndContainment(t *testing.T) {
	db, ctx, base := dbtest.New(t), context.Background(), t.TempDir()
	user := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('root-agent', 'Agent', '')`); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	req := WorkspaceRequest{UserID: user, AgentID: "root-agent"}
	r, err := m.OpenRoot(ctx, req, RootAgentWorkspace, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Mkdir(ctx, "dir/sub", 0o755, MkdirOptions{Parents: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Write(ctx, "dir/sub/file", strings.NewReader("one"), WriteOptions{Mode: 0o640, Sync: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Write(ctx, "dir/sub/file", strings.NewReader("two"), WriteOptions{Append: true}); err != nil {
		t.Fatal(err)
	}
	if info, err := r.Stat(ctx, "dir/sub/file"); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	var out bytes.Buffer
	if err := r.Read(ctx, "dir/sub/file", &out, ReadOptions{MaxBytes: 6}); err != nil || out.String() != "onetwo" {
		t.Fatalf("read = %q, %v", out.String(), err)
	}
	limited := &bytes.Buffer{}
	if err := r.Read(ctx, "dir/sub/file", limited, ReadOptions{MaxBytes: 5}); !errors.Is(err, ErrReadLimit) || limited.Len() != 5 {
		t.Fatalf("limit = %v", err)
	}
	if err := r.Write(ctx, "dir/sub/second", strings.NewReader("two"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(ctx, "dir/sub", ListOptions{Limit: 1}); !errors.Is(err, ErrListLimit) {
		t.Fatalf("list limit = %v", err)
	}
	if entries, err := r.List(ctx, "dir/sub", ListOptions{Limit: 2}); err != nil || len(entries) != 2 {
		t.Fatalf("list = %v, %v", entries, err)
	}
	if entries, err := r.List(ctx, "dir/sub", ListOptions{Limit: math.MaxInt}); err != nil || len(entries) != 2 {
		t.Fatalf("max-limit list = %v, %v", entries, err)
	}
	if err := r.Rename(ctx, "dir/sub/file", "dir/sub/renamed", RenameOptions{SyncParent: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/renamed", filepath.Join(base, "users", user, "agents", "root-agent", "dir", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stat(ctx, "dir/link"); err != nil {
		t.Fatalf("contained symlink: %v", err)
	}
	if err := os.Symlink("../../../../../../etc/passwd", filepath.Join(base, "users", user, "agents", "root-agent", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stat(ctx, "escape"); err == nil {
		t.Fatal("escaping symlink succeeded")
	}
	if err := r.Remove(ctx, "dir", RemoveOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := m.OpenRoot(ctx, req, RootPrincipalData, RootReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	if err := ro.Write(ctx, "x", strings.NewReader("x"), WriteOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only = %v", err)
	}
	if err := ro.Mkdir(ctx, "x", 0o755, MkdirOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only mkdir = %v", err)
	}
	if err := ro.Remove(ctx, "x", RemoveOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only remove = %v", err)
	}
	if err := ro.Rename(ctx, "x", "y", RenameOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only rename = %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRootSelectsExactTypedScopes(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user, agentID := uuid.NewString(), "scope-agent"
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
	for _, tc := range []struct {
		name  string
		req   WorkspaceRequest
		scope RootScope
		path  string
	}{
		{name: "agent workspace", req: WorkspaceRequest{UserID: user, AgentID: agentID}, scope: RootAgentWorkspace, path: filepath.Join(base, "users", user, "agents", agentID)},
		{name: "principal data", req: WorkspaceRequest{UserID: user, AgentID: agentID}, scope: RootPrincipalData, path: filepath.Join(base, "users", user, "data")},
		{name: "system skills", scope: RootSystemSkills, path: filepath.Join(base, ".agents", "db-skills")},
		{name: "system agent skills", req: WorkspaceRequest{AgentID: agentID}, scope: RootSystemAgentSkills, path: filepath.Join(base, "agents", agentID, ".agents", "skills")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := m.OpenRoot(ctx, tc.req, tc.scope, RootReadWrite)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Write(ctx, "marker", strings.NewReader(tc.name), WriteOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(tc.path, "marker")); err != nil || string(got) != tc.name {
				t.Fatalf("typed root marker = %q, %v", got, err)
			}
		})
	}
}

func TestRootPinsTypedDirectoryAndFencesOwnerDeletion(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user, agentID := uuid.NewString(), "pinned-agent"
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
	if err := r.Write(ctx, "original", strings.NewReader("old inode"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(base, "users", user, "agents", agentID)
	movedPath := rootPath + ".moved"
	if err := os.Rename(rootPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "replacement"), []byte("new inode"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := r.Read(ctx, "original", &got, ReadOptions{MaxBytes: 32}); err != nil || got.String() != "old inode" {
		t.Fatalf("pinned root read = %q, %v", got.String(), err)
	}

	fence := &signalingFence{acquired: make(chan struct{})}
	deletion, err := NewOwnerDeletion(db, m, fence)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- deletion.DeleteUser(ctx, user, "actor") }()
	select {
	case <-fence.acquired:
	case <-time.After(time.Second):
		t.Fatal("owner deletion did not reach the local owner gate")
	}
	select {
	case err := <-deleted:
		t.Fatalf("owner deletion passed active root: %v", err)
	default:
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner deletion did not continue after Root.Close")
	}
}

type signalingFence struct {
	acquired chan struct{}
	once     sync.Once
}

func (f *signalingFence) AcquireHomeOwnerFence(context.Context, OwnerKind, string) (OwnerFenceLease, error) {
	f.once.Do(func() { close(f.acquired) })
	return &testFenceLease{}, nil
}

type cancelAfterChunkReader struct {
	cancel context.CancelFunc
	sent   bool
}

func (r *cancelAfterChunkReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	n := copy(p, "partial")
	r.cancel()
	return n, nil
}

func TestRootInterruptedWriteReportsOutcomeUnknown(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user, agentID := uuid.NewString(), "cancel-agent"
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
	writeCtx, cancel := context.WithCancel(ctx)
	err = r.Write(writeCtx, "partial", &cancelAfterChunkReader{cancel: cancel}, WriteOptions{})
	if !IsOutcomeUnknown(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted write = %v", err)
	}
}

func TestRootInterruptedUploadDoesNotPublishPartialTarget(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user, agentID := uuid.NewString(), "cancel-upload-agent"
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
	root, err := m.OpenRoot(ctx, WorkspaceRequest{UserID: user, AgentID: agentID}, RootAgentWorkspace, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	uploadCtx, cancel := context.WithCancel(ctx)
	err = root.Upload(uploadCtx, "published", &cancelAfterChunkReader{cancel: cancel}, WriteOptions{MaxBytes: 1024})
	if !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) {
		t.Fatalf("interrupted upload = %v, want known unpublished cancellation", err)
	}
	if _, err := root.Stat(ctx, "published"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial upload target stat = %v, want not exist", err)
	}
}

func TestRootUploadLimitDoesNotPublishPartialTarget(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('limit-agent', 'Agent', '')`); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	root, err := m.OpenRoot(ctx, WorkspaceRequest{UserID: user, AgentID: "limit-agent"}, RootAgentWorkspace, RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	err = root.Upload(ctx, "target", strings.NewReader("too large"), WriteOptions{MaxBytes: 3})
	if !errors.Is(err, ErrUploadLimit) {
		t.Fatalf("upload error=%v", err)
	}
	if _, err := root.Stat(ctx, "target"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target stat=%v", err)
	}
}

type blockingChunkReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingChunkReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return copy(p, "complete"), io.EOF
}

func TestRootCloseAndOwnerDeletionWaitForActiveWrite(t *testing.T) {
	db, ctx, base := dbtest.New(t), t.Context(), t.TempDir()
	user, agentID := uuid.NewString(), "active-write-agent"
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
	reader := &blockingChunkReader{started: make(chan struct{}), release: make(chan struct{})}
	written := make(chan error, 1)
	go func() { written <- r.Write(ctx, "file", reader, WriteOptions{}) }()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	fence := &signalingFence{acquired: make(chan struct{})}
	deletion, err := NewOwnerDeletion(db, m, fence)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- deletion.DeleteUser(ctx, user, "actor") }()
	select {
	case <-fence.acquired:
	case <-time.After(time.Second):
		t.Fatal("deletion did not reach owner gate")
	}
	select {
	case err := <-closed:
		t.Fatalf("Root.Close passed active write: %v", err)
	case err := <-deleted:
		t.Fatalf("owner deletion passed active write: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(reader.release)
	for name, result := range map[string]<-chan error{"write": written, "close": closed, "delete": deleted} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
}

func TestOpenRootRejectsCanceledSystemScopeWithoutMutation(t *testing.T) {
	base := t.TempDir()
	m, err := NewWorkspaceManager(dbtest.New(t), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.OpenRoot(ctx, WorkspaceRequest{}, RootSystemSkills, RootReadWrite); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenRoot error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled OpenRoot mutated filesystem: %v", err)
	}
}

func TestOpenRootMissingOwnerDoesNotMaterialize(t *testing.T) {
	db, base := dbtest.New(t), t.TempDir()
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.OpenRoot(context.Background(), WorkspaceRequest{UserID: "missing", AgentID: "missing"}, RootAgentWorkspace, RootReadWrite)
	if err == nil {
		t.Fatal("OpenRoot succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(base, "users")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("filesystem mutated: %v", statErr)
	}
}
