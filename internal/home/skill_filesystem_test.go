package home

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestUseSkillFilesystemMapsExactCatalogRoots(t *testing.T) {
	r, local := newRegistry(t)
	ctx := context.Background()
	userID, agentID := "user-a", "agent-a"
	user, err := UserSkillCatalog(userID)
	if err != nil {
		t.Fatal(err)
	}
	userAgent, err := UserAgentSkillCatalog(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	systemAgent, err := SystemAgentSkillCatalog(agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		root *SkillRoot
		want string
	}{
		{name: "system", root: SystemSkillCatalog(), want: filepath.Join(".agents", "db-skills")},
		{name: "system agent", root: systemAgent, want: filepath.Join("agents", agentID, ".agents", "skills")},
		{name: "user", root: user, want: filepath.Join("users", userID, "data", ".agents", "skills")},
		{name: "user agent", root: userAgent, want: filepath.Join("users", userID, "agents", agentID, ".agents", "skills")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := tc.name + ".txt"
			err := r.UseSkillFilesystem(ctx, tc.root, func(filesystem sandbox.Filesystem) error {
				return filesystem.Write(ctx, sandbox.PathWorkspace+"/"+marker, strings.NewReader(tc.name), sandbox.WriteOptions{})
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(local.base, tc.want, marker))
			if err != nil || string(got) != tc.name {
				t.Fatalf("catalog mapping %q = %q, %v", tc.want, got, err)
			}
		})
	}
	// A catalog is its subroot, not its parent Home. The user catalog cannot
	// walk into user-agent data even though both live beneath the same user.
	err = r.UseSkillFilesystem(ctx, user, func(filesystem sandbox.Filesystem) error {
		return filesystem.Write(ctx, sandbox.PathWorkspace+"/../agents/agent-a/.agents/skills/escape", strings.NewReader("x"), sandbox.WriteOptions{})
	})
	if err == nil {
		t.Fatal("user catalog escaped into agent Home")
	}
}

func TestUseSkillFilesystemRejectsSameStoreCatalogAliases(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		alias func(*testing.T, *LocalStore)
	}{
		{
			name: "Home locator ancestor",
			alias: func(t *testing.T, local *LocalStore) {
				t.Helper()
				if err := os.RemoveAll(filepath.Join(local.base, "users", "a")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("b", filepath.Join(local.base, "users", "a")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "catalog ancestor",
			alias: func(t *testing.T, local *LocalStore) {
				t.Helper()
				if err := os.RemoveAll(filepath.Join(local.base, "users", "a", "data")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../b/data", filepath.Join(local.base, "users", "a", "data")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, local := newRegistry(t)
			a, err := UserSkillCatalog("a")
			if err != nil {
				t.Fatal(err)
			}
			b, err := UserSkillCatalog("b")
			if err != nil {
				t.Fatal(err)
			}
			// Materialize both catalog trees before swapping A's component. The
			// persisted A record remains ready, so access must reject its alias.
			for _, root := range []*SkillRoot{a, b} {
				if err := r.UseSkillFilesystem(ctx, root, func(sandbox.Filesystem) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			tc.alias(t, local)
			called := false
			err = r.UseSkillFilesystem(ctx, a, func(filesystem sandbox.Filesystem) error {
				called = true
				return filesystem.Write(ctx, sandbox.PathWorkspace+"/stolen", strings.NewReader("no"), sandbox.WriteOptions{})
			})
			if err == nil || called {
				t.Fatalf("aliased catalog access err=%v called=%t", err, called)
			}
			if _, err := os.Stat(filepath.Join(local.base, "users", "b", "data", ".agents", "skills", "stolen")); !os.IsNotExist(err) {
				t.Fatalf("B catalog received aliased bytes: %v", err)
			}
		})
	}
}

func TestSkillRootRejectsUnsupportedAndMalformedScopesBeforeOpening(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &countingSharedSkillStore{Store: local, local: local}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SystemAgentSkillCatalog("../agent"); err == nil {
		t.Fatal("malformed system Agent root constructed")
	}
	if _, err := UserSkillCatalog("../user"); err == nil {
		t.Fatal("malformed user root constructed")
	}
	if _, err := newSkillRoot(Principal(GroupPrincipal, "group")); err == nil {
		t.Fatal("group Skill root constructed")
	}
	if err := r.UseSkillFilesystem(context.Background(), nil, func(sandbox.Filesystem) error { return nil }); err == nil {
		t.Fatal("nil root opened a filesystem")
	}
	if err := r.UseSkillFilesystem(context.Background(), SystemSkillCatalog(), nil); err == nil {
		t.Fatal("nil callback opened a filesystem")
	}
	if err := r.UseSkillFilesystem(context.Background(), &SkillRoot{key: Principal(GroupPrincipal, "group")}, func(sandbox.Filesystem) error { return nil }); err == nil {
		t.Fatal("forged group root opened a filesystem")
	}
	if got := store.opened.Load(); got != 0 {
		t.Fatalf("opened %d filesystems for rejected roots", got)
	}
}

func TestUseSkillFilesystemFailsClosedWithoutPrivateStoreCapability(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(dbtest.New(t), local.ID(), struct{ Store }{Store: local})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UseSkillFilesystem(context.Background(), SystemSkillCatalog(), func(sandbox.Filesystem) error { return nil }); err == nil {
		t.Fatal("Store without private Skill filesystem capability succeeded")
	}
}

type blockingSkillStore struct {
	Store
	local   *LocalStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSkillStore) openSkillFilesystem(record Record, root *SkillRoot) (sandbox.Filesystem, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.local.openSkillFilesystem(record, root)
}

func TestUseSkillFilesystemBlocksOwnerDeletionDuringCallback(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		root   func(string, string) (*SkillRoot, error)
		delete func(*OwnerDeletion, string, string) error
	}{
		{
			name:   "user catalog/user deletion",
			root:   func(userID, _ string) (*SkillRoot, error) { return UserSkillCatalog(userID) },
			delete: func(d *OwnerDeletion, userID, _ string) error { return d.DeleteUser(ctx, userID, "operator") },
		},
		{
			name:   "user Agent catalog/user deletion",
			root:   UserAgentSkillCatalog,
			delete: func(d *OwnerDeletion, userID, _ string) error { return d.DeleteUser(ctx, userID, "operator") },
		},
		{
			name:   "user Agent catalog/Agent deletion",
			root:   UserAgentSkillCatalog,
			delete: func(d *OwnerDeletion, _, agentID string) error { return d.DeleteAgent(ctx, agentID, "operator") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtest.New(t)
			local, err := NewLocalStore("local", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRegistry(db, local.ID(), local)
			if err != nil {
				t.Fatal(err)
			}
			userID, agentID := uuid.NewString(), uuid.NewString()
			if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
				t.Fatal(err)
			}
			if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			deletion, err := NewOwnerDeletion(db, r, &recordingOwnerEnqueue{}, &recordingOwnerFencer{})
			if err != nil {
				t.Fatal(err)
			}
			root, err := tc.root(userID, agentID)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			useDone := make(chan error, 1)
			go func() {
				useDone <- r.UseSkillFilesystem(ctx, root, func(sandbox.Filesystem) error {
					close(entered)
					<-release
					return nil
				})
			}()
			<-entered
			lockAttempted := make(chan struct{})
			r.beforeOwnerLockAcquire = func() { close(lockAttempted) }
			deleteDone := make(chan error, 1)
			go func() { deleteDone <- tc.delete(deletion, userID, agentID) }()
			select {
			case <-lockAttempted:
			case <-time.After(time.Second):
				t.Fatal("deletion did not reach owner-lock acquisition")
			}
			select {
			case err := <-deleteDone:
				t.Fatalf("deletion completed during Skill callback: %v", err)
			default:
			}
			close(release)
			if err := <-useDone; err != nil {
				t.Fatal(err)
			}
			if err := <-deleteDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUseSkillFilesystemDoesNotRetainDBTransactionDuringFilesystemIO(t *testing.T) {
	base := dbtest.New(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 1
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingSkillStore{Store: local, local: local, entered: make(chan struct{}), release: make(chan struct{})}
	r, err := NewRegistry(db, local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- r.UseSkillFilesystem(context.Background(), SystemSkillCatalog(), func(sandbox.Filesystem) error { return nil })
	}()
	<-store.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("database unavailable while filesystem open blocks: %v", err)
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Compile-time boundary check: callers get only the bounded Filesystem. The
// public root is opaque and cannot expose a Home, attachment, locator, or path.
var _ func(*Registry, context.Context, *SkillRoot, func(sandbox.Filesystem) error) error = (*Registry).UseSkillFilesystem
