package home

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestUseSharedSkillFilesystemUsesWritableCanonicalWorkspace(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	var retained sandbox.Filesystem
	err := r.UseSharedSkillFilesystem(ctx, SystemSkills(), func(filesystem sandbox.Filesystem) error {
		retained = filesystem
		if err := filesystem.Mkdir(ctx, sandbox.PathWorkspace+"/example", 0o755); err != nil {
			return err
		}
		if err := filesystem.Write(ctx, sandbox.PathWorkspace+"/example/SKILL.md", strings.NewReader("shared"), sandbox.WriteOptions{}); err != nil {
			return err
		}
		reader, _, err := filesystem.Read(ctx, sandbox.PathWorkspace+"/example/SKILL.md", sandbox.ReadOptions{MaxBytes: 64})
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(reader)
		err = errors.Join(readErr, reader.Close())
		if err != nil || string(data) != "shared" {
			return errors.New("shared Skill bytes did not round-trip")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("UseSharedSkillFilesystem: %v", err)
	}
	if _, err := retained.Stat(ctx, sandbox.PathWorkspace+"/example/SKILL.md"); err == nil {
		t.Fatal("filesystem remained usable after its callback")
	}
	attachment, err := r.Resolve(ctx, SystemSkills(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !attachment.ReadOnly {
		t.Fatal("ordinary shared Skill attachment became writable")
	}
}

type countingSharedSkillStore struct {
	Store
	local  *LocalStore
	opened atomic.Int32
}

func (s *countingSharedSkillStore) openSkillFilesystem(record Record, root *SkillRoot) (sandbox.Filesystem, error) {
	s.opened.Add(1)
	return s.local.openSkillFilesystem(record, root)
}

func TestUseSharedSkillFilesystemRejectsInvalidInputsBeforeOpening(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &countingSharedSkillStore{Store: local, local: local}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		key  Key
		use  func(sandbox.Filesystem) error
	}{
		{name: "principal", key: Principal(UserPrincipal, "user"), use: func(sandbox.Filesystem) error { return nil }},
		{name: "malformed agent", key: SystemAgentSkills("../agent"), use: func(sandbox.Filesystem) error { return nil }},
		{name: "nil callback", key: SystemSkills()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.UseSharedSkillFilesystem(context.Background(), tc.key, tc.use); err == nil {
				t.Fatal("invalid shared Skill filesystem request succeeded")
			}
		})
	}
	if got := store.opened.Load(); got != 0 {
		t.Fatalf("opened %d filesystems for rejected inputs", got)
	}
}

func TestUseSharedSkillFilesystemFailsClosedWithoutStoreCapability(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(dbtest.New(t), local.ID(), struct{ Store }{Store: local})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UseSharedSkillFilesystem(context.Background(), SystemSkills(), func(sandbox.Filesystem) error { return nil }); err == nil {
		t.Fatal("Store without the private filesystem capability succeeded")
	}
}

func TestUseSharedSkillFilesystemRejectsNilStoreFilesystem(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &fixedFilesystemStore{Store: local}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = r.UseSharedSkillFilesystem(context.Background(), SystemSkills(), func(sandbox.Filesystem) error {
		called = true
		return nil
	})
	if err == nil || called || store.opens != 1 {
		t.Fatalf("error=%v called=%t opens=%d; want nil filesystem rejection before callback", err, called, store.opens)
	}
}

func TestSharedSkillFilesystemRevalidatesRecordIdentityAndState(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	record, err := r.Ensure(ctx, SystemSkills())
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []Record{
		{ID: record.ID, Key: record.Key, StoreID: "other", Locator: record.Locator, State: StateReady},
		{ID: record.ID, Key: record.Key, StoreID: record.StoreID, Locator: "other", State: StateReady},
	} {
		if _, err := r.sharedSkillFilesystemStore(ctx, stale); err == nil {
			t.Fatalf("stale record %+v opened a shared Skill filesystem", stale)
		}
	}
	if _, err := r.Tombstone(ctx, record.Key, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sharedSkillFilesystemStore(ctx, record); err == nil {
		t.Fatal("tombstoned shared Skill root opened a filesystem")
	}
}

type closeCountingFilesystem struct {
	sandbox.Filesystem
	closeErr error
	closes   int
}

func (f *closeCountingFilesystem) Close() error {
	f.closes++
	return f.closeErr
}

type fixedFilesystemStore struct {
	Store
	filesystem sandbox.Filesystem
	opens      int
}

func (s *fixedFilesystemStore) openSkillFilesystem(Record, *SkillRoot) (sandbox.Filesystem, error) {
	s.opens++
	return s.filesystem, nil
}

func TestUseSharedSkillFilesystemClosesOnceAndPreservesFailure(t *testing.T) {
	callbackErr, closeErr := errors.New("callback failed"), errors.New("close failed")
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filesystem := &closeCountingFilesystem{closeErr: closeErr}
	store := &fixedFilesystemStore{Store: local, filesystem: filesystem}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	err = r.UseSharedSkillFilesystem(context.Background(), SystemSkills(), func(sandbox.Filesystem) error { return callbackErr })
	if !errors.Is(err, callbackErr) || !errors.Is(err, closeErr) || filesystem.closes != 1 {
		t.Fatalf("error = %v, closes = %d; want both errors and one close", err, filesystem.closes)
	}
}

func TestUseSharedSkillFilesystemClosesOnceAndPreservesPanic(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filesystem := &closeCountingFilesystem{}
	store := &fixedFilesystemStore{Store: local, filesystem: filesystem}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if got := recover(); got != "callback panic" || filesystem.closes != 1 {
			t.Fatalf("panic = %#v, closes = %d", got, filesystem.closes)
		}
	}()
	_ = r.UseSharedSkillFilesystem(context.Background(), SystemSkills(), func(sandbox.Filesystem) error { panic("callback panic") })
}

func TestUseSharedSkillFilesystemCanceledBeforeCallbackDoesNotOpen(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filesystem := &closeCountingFilesystem{}
	store := &fixedFilesystemStore{Store: local, filesystem: filesystem}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = r.UseSharedSkillFilesystem(ctx, SystemSkills(), func(sandbox.Filesystem) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called || store.opens != 0 || filesystem.closes != 0 {
		t.Fatalf("error=%v called=%t opens=%d closes=%d", err, called, store.opens, filesystem.closes)
	}
}

func TestUseSharedAgentSkillFilesystemBlocksAgentPurgeDuringCallback(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &countingSharedSkillStore{Store: local, local: local}
	r, err := NewRegistry(db, local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	deletion, err := NewOwnerDeletion(db, r, &recordingOwnerEnqueue{}, &recordingOwnerFencer{})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	useDone := make(chan error, 1)
	go func() {
		useDone <- r.UseSharedSkillFilesystem(ctx, SystemAgentSkills(agentID), func(sandbox.Filesystem) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	deleteLockAttempted := make(chan struct{})
	r.beforeOwnerLockAcquire = func() { close(deleteLockAttempted) }
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- deletion.DeleteAgent(ctx, agentID, "operator") }()
	select {
	case <-deleteLockAttempted:
	case <-time.After(time.Second):
		t.Fatal("Agent deletion did not reach the owner-lock acquisition")
	}
	select {
	case err := <-deleteDone:
		t.Fatalf("Agent deletion completed during shared Skill callback: %v", err)
	default:
	}
	close(release)
	if err := <-useDone; err != nil {
		t.Fatalf("UseSharedSkillFilesystem: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
}

// Compile-time boundary check: callers receive only canonical filesystem
// operations, not a path, attachment, locator, or Record.
var _ func(*Registry, context.Context, Key, func(sandbox.Filesystem) error) error = (*Registry).UseSharedSkillFilesystem
