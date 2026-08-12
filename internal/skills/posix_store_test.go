package skills

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type posixStoreFixture struct {
	store   *POSIXStore
	manager *home.WorkspaceManager
	base    string
	userID  string
	agentID string
}

func newPOSIXStoreFixture(t *testing.T) posixStoreFixture {
	t.Helper()
	db := dbtest.New(t)
	userID, agentID := uuid.NewString(), "home-skill-agent"
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Home Skill Agent','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store, err := NewPOSIXStore(db, manager)
	if err != nil {
		t.Fatal(err)
	}
	return posixStoreFixture{store: store, manager: manager, base: base, userID: userID, agentID: agentID}
}

func TestManagedSkillLockAcquireUnknownDiscardsSession(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	var released, discarded atomic.Int32
	f.store.acquireManagedLock = func(context.Context) (managedSkillLockSession, error) {
		return managedSkillLockSession{
			lock:    func(context.Context) error { return errors.New("ack lost after server lock") },
			release: func() { released.Add(1) },
			discard: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("discard did not receive a fresh bounded context")
				}
				discarded.Add(1)
				return nil
			},
		}, nil
	}
	if _, err := f.store.lockManagedMutations(t.Context()); !home.IsOutcomeUnknown(err) {
		t.Fatalf("uncertain acquire = %v", err)
	}
	if released.Load() != 0 || discarded.Load() != 1 {
		t.Fatalf("uncertain acquired session release=%d discard=%d", released.Load(), discarded.Load())
	}
}

func TestManagedSkillLockUnlockTimeoutDiscardsSession(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	var released, discarded atomic.Int32
	f.store.cleanupContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), time.Millisecond)
	}
	f.store.acquireManagedLock = func(context.Context) (managedSkillLockSession, error) {
		return managedSkillLockSession{
			lock: func(context.Context) error { return nil },
			unlock: func(ctx context.Context) (bool, error) {
				<-ctx.Done()
				return false, ctx.Err()
			},
			release: func() { released.Add(1) },
			discard: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("discard did not receive a fresh bounded context")
				}
				discarded.Add(1)
				return nil
			},
		}, nil
	}
	release, err := f.store.lockManagedMutations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); !home.IsOutcomeUnknown(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain unlock = %v", err)
	}
	if released.Load() != 0 || discarded.Load() != 1 {
		t.Fatalf("uncertain unlock session release=%d discard=%d", released.Load(), discarded.Load())
	}
}

func fixtureSkill(f posixStoreFixture, id, scope string) Skill {
	skill := Skill{ID: id, Scope: scope, Name: id, Description: "Home current state"}
	switch scope {
	case "system_agent":
		skill.AgentID = f.agentID
	case "user":
		skill.UserID = f.userID
	case "user_agent":
		skill.UserID, skill.AgentID = f.userID, f.agentID
	}
	return skill
}

func TestPOSIXStoreUsesTypedRootsAndLeavesPostgresByteFree(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	for _, test := range []struct {
		scope, id string
		root      string
	}{
		{"system", "system-skill", filepath.Join(f.base, ".agents", "db-skills")},
		{"system_agent", "system-agent-skill", filepath.Join(f.base, "agents", f.agentID, ".agents", "skills")},
		{"user", "user-skill", filepath.Join(f.base, "users", f.userID, ".agents", "skills")},
		{"user_agent", "user-agent-skill", filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID)},
	} {
		t.Run(test.scope, func(t *testing.T) {
			snapshot, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, test.id, test.scope), map[string]string{
				MainFile: "main", "references/data.bin": string([]byte{0, 0xff, 'x'}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !validSkillDigest(snapshot.Skill.ContentDigest) || snapshot.Skill.Description != "Home current state" {
				t.Fatalf("snapshot = %#v", snapshot.Skill)
			}
			target, err := os.Readlink(filepath.Join(test.root, test.id))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.ToSlash(filepath.Join(managedRevisionRoot, test.id, snapshot.Skill.ContentDigest)); target != want {
				t.Fatalf("selector = %q, want %q", target, want)
			}
			loaded, err := f.store.LoadFile(t.Context(), test.id, "references/data.bin")
			if err != nil || loaded != string([]byte{0, 0xff, 'x'}) {
				t.Fatalf("binary load = %q, %v", loaded, err)
			}
		})
	}

	var fileRows int
	if err := f.store.db.QueryRow(t.Context(), "SELECT count(*) FROM skill_file").Scan(&fileRows); err != nil || fileRows != 0 {
		t.Fatalf("PostgreSQL skill_file rows = %d, %v", fileRows, err)
	}
	rows, err := f.store.db.Query(t.Context(), "SELECT description,status,disable_model_invocation,metadata::text FROM skill")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var description, status, metadata string
		var disabled bool
		if err := rows.Scan(&description, &status, &disabled, &metadata); err != nil {
			t.Fatal(err)
		}
		if description != "" || status != SkillStatusActive || disabled || metadata != "{}" {
			t.Fatalf("PostgreSQL retained mutable state: %q %q %v %q", description, status, disabled, metadata)
		}
	}
}

func TestPOSIXStoreDigestCASHasOneConcurrentWinner(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "cas-skill", "user_agent"), map[string]string{MainFile: "before"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, content := range []string{"first", "second"} {
		go func() {
			<-start
			_, err := f.store.UpdateManagedSkill(context.Background(), ManagedSkillUpdate{
				ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID, AgentID: created.Skill.AgentID,
				ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: content},
			})
			errs <- err
		}()
	}
	close(start)
	var succeeded, conflicted int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSkillDigestConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if _, err := f.store.UpdateManagedSkill(t.Context(), ManagedSkillUpdate{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID, AgentID: created.Skill.AgentID,
		ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: "stale"},
	}); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
}

type faultRootOpener struct {
	base              home.RootOpener
	closeFailure      atomic.Bool
	selectorSyncError atomic.Bool
	rootSyncAlwaysErr atomic.Bool
	rootSyncCalls     atomic.Int32
}

func (o *faultRootOpener) OpenRoot(ctx context.Context, request home.WorkspaceRequest, scope home.RootScope, access home.RootAccess) (home.RootOperations, error) {
	root, err := o.base.OpenRoot(ctx, request, scope, access)
	if err != nil {
		return nil, err
	}
	return &faultSkillRoot{SkillRootOperations: root.(home.SkillRootOperations), opener: o}, nil
}

func (o *faultRootOpener) OpenExistingRoot(ctx context.Context, request home.WorkspaceRequest, scope home.RootScope) (home.RootOperations, error) {
	base, ok := o.base.(home.ExistingRootOpener)
	if !ok {
		return nil, errors.New("test root cannot open existing roots")
	}
	root, err := base.OpenExistingRoot(ctx, request, scope)
	if err != nil {
		return nil, err
	}
	return &faultSkillRoot{SkillRootOperations: root.(home.SkillRootOperations), opener: o}, nil
}

type faultSkillRoot struct {
	home.SkillRootOperations
	opener *faultRootOpener
	once   sync.Once
}

func (r *faultSkillRoot) SyncDirectory(ctx context.Context, directory string) error {
	if directory == "." && r.opener.rootSyncAlwaysErr.Load() {
		return errors.New("injected persistent root fsync failure")
	}
	if directory == "." && r.opener.rootSyncCalls.Add(1) == 2 && r.opener.selectorSyncError.CompareAndSwap(true, false) {
		return errors.New("injected selector fsync failure")
	}
	return r.SkillRootOperations.SyncDirectory(ctx, directory)
}

func (r *faultSkillRoot) Close() error {
	err := r.SkillRootOperations.Close()
	r.once.Do(func() {
		if r.opener.closeFailure.CompareAndSwap(true, false) {
			err = errors.Join(err, errors.New("injected capability close acknowledgement failure"))
		}
	})
	return err
}

func TestPOSIXStoreReconcilesExactDigestAfterCloseAcknowledgementFailure(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.closeFailure.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "close-reconcile", "user"), map[string]string{MainFile: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	if content, err := store.LoadFileRevision(t.Context(), snapshot.Skill.ID, MainFile, snapshot.Skill.ContentDigest); err != nil || content != "committed" {
		t.Fatalf("exact reconciled revision = %q, %v", content, err)
	}
}

func TestPOSIXStoreReconcilesFailedSelectorFsyncBeforeRegisteringIdentity(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.selectorSyncError.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "fsync-reconciled", "user_agent"), map[string]string{MainFile: "committed"})
	if err != nil || !validSkillDigest(created.Skill.ContentDigest) {
		t.Fatalf("reconciled selector durability = %#v, %v", created, err)
	}
	if identity, getErr := store.GetIdentity(t.Context(), "fsync-reconciled"); getErr != nil || identity == nil {
		t.Fatalf("identity absent after exact reconciliation: %#v, %v", identity, getErr)
	}
}

func TestPOSIXStorePersistentPublicationFenceFailureIsOutcomeUnknown(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.rootSyncAlwaysErr.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "fsync-unknown", "user_agent"), map[string]string{MainFile: "uncertain"}); !home.IsOutcomeUnknown(err) {
		t.Fatalf("persistent publication uncertainty = %v", err)
	}
	if identity, getErr := store.GetIdentity(t.Context(), "fsync-unknown"); getErr != nil || identity != nil {
		t.Fatalf("identity registered after uncertain publication: %#v, %v", identity, getErr)
	}
}

func TestPOSIXStoreExactUpdateRetryCompletesEvidenceAfterSelectorOutcomeUnknown(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "update-reconcile", "user"), map[string]string{MainFile: "before"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.loadIdentity(t.Context(), created.Skill)
	if err != nil {
		t.Fatal(err)
	}
	update := ManagedSkillUpdate{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID,
		ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: "after"},
	}
	after := before.Skill
	after.Version++
	after.UpdatedAt = store.now().UTC()
	files, err := mergeRevisionFiles(before.Files, update.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.publish(t.Context(), after, files, update.ExpectedDigest, false); err != nil {
		t.Fatalf("simulate publication before evidence: %v", err)
	}
	retried, err := store.UpdateManagedSkill(t.Context(), update)
	if err != nil || retried.Skill.ContentDigest == created.Skill.ContentDigest {
		t.Fatalf("exact update retry = %#v, %v", retried, err)
	}
}

type projectionReader struct {
	identities []Skill
	revisions  map[string]ManagedRevision
	loads      int
}

func (r *projectionReader) GetIdentity(_ context.Context, id string) (*Skill, error) {
	for i := range r.identities {
		if r.identities[i].ID == id {
			return &r.identities[i], nil
		}
	}
	return nil, nil
}

func (r *projectionReader) ListIdentityVisible(context.Context, ViewContext) ([]Skill, error) {
	return append([]Skill(nil), r.identities...), nil
}

func (r *projectionReader) ListIdentityByScope(context.Context, string, string, string) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) ListIdentityCandidate(context.Context, string, ViewContext) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) LoadCurrentRevision(_ context.Context, identity Skill) (ManagedRevision, error) {
	r.loads++
	revision, ok := r.revisions[identity.ID]
	if !ok {
		return ManagedRevision{}, os.ErrNotExist
	}
	return revision, nil
}

func (r *projectionReader) LoadExactRevision(_ context.Context, identity Skill, digest string) (ManagedRevision, error) {
	revision, err := r.LoadCurrentRevision(context.Background(), identity)
	if err != nil || revision.Skill.ContentDigest != digest {
		return ManagedRevision{}, errors.Join(err, ErrSkillDigestConflict)
	}
	return revision, nil
}

type projectionSession struct {
	tempVisible string
	tempHost    string
}

func (s projectionSession) Policy() pkgsandbox.Policy {
	return pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvTempDir: s.tempVisible}}
}

func (s projectionSession) ResolveWritePath(name string) (string, error) {
	rel, err := filepath.Rel(s.tempVisible, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return filepath.Join(s.tempHost, rel), nil
}

type selectedSkillReads struct{ denied map[string]bool }

func (a selectedSkillReads) BeginRead(context.Context) (SkillReadDecision, error) { return a, nil }

func (a selectedSkillReads) AllowRead(_ context.Context, id, _, _, _ string) (bool, error) {
	return !a.denied[id], nil
}

func projectionSkill(id, name, digest string) Skill {
	return Skill{ID: id, Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: name, Status: SkillStatusActive, Version: 1, ContentDigest: digest}
}

func TestManagedLoadAuthorizesIdentityBeforeHomeAndProjectsExactRevision(t *testing.T) {
	digest := strings.Repeat("a", 64)
	identity := projectionSkill("allowed-id", "allowed", "")
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{identity.ID: {
			Skill: projectionSkill(identity.ID, identity.Name, digest),
			Files: map[string][]byte{MainFile: []byte("current"), "scripts/support.sh": []byte("#!/bin/sh\nprintf support-ok")},
			Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/support.sh": 0o755},
		}},
	}
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	denied := NewTool(nil, "", "").WithManagedRevisions(reader, session).WithReadAuthorizer(selectedSkillReads{denied: map[string]bool{identity.ID: true}})
	if _, err := denied.load(t.Context(), map[string]any{"name": identity.Name}); !errors.Is(err, errSkillNotFound) {
		t.Fatalf("denied load = %v", err)
	}
	if reader.loads != 0 {
		t.Fatalf("Home revision opened before authorization: loads=%d", reader.loads)
	}

	tool := NewTool(nil, "", "").WithManagedRevisions(reader, session).WithReadAuthorizer(selectedSkillReads{})
	out, err := tool.load(t.Context(), map[string]any{"name": identity.Name})
	if err != nil {
		t.Fatal(err)
	}
	wantVisible := filepath.Join(session.tempVisible, "stella-skills", identity.Scope, identity.ID, digest)
	if !strings.Contains(out, "<skill_dir>"+wantVisible+"</skill_dir>") || !strings.Contains(out, "current") {
		t.Fatalf("load output = %q", out)
	}
	host := filepath.Join(session.tempHost, "stella-skills", identity.Scope, identity.ID, digest)
	if content, err := os.ReadFile(filepath.Join(host, "scripts", "support.sh")); err != nil || string(content) != "#!/bin/sh\nprintf support-ok" {
		t.Fatalf("projected support file = %q, %v", content, err)
	}
	if info, err := os.Stat(filepath.Join(host, "scripts", "support.sh")); err != nil || info.Mode().Perm()&0o222 != 0 || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("projected support mode = %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(host))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Fatalf("projection exposed unpublished stage %q", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(session.tempHost, managedRevisionRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authority catalog appeared in Session projection: %v", err)
	}
}

func TestManagedProjectionConcurrentExactDigestPublishesOneCompletePath(t *testing.T) {
	digest := strings.Repeat("f", 64)
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	tool := NewTool(nil, "", "").WithManagedRevisions(nil, session)
	revision := ManagedRevision{
		Skill: projectionSkill("concurrent-id", "concurrent", digest),
		Files: map[string][]byte{
			MainFile:             []byte("# Concurrent\n"),
			"scripts/support.sh": []byte("#!/bin/sh\nprintf complete"),
		},
	}

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	paths := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			projected, err := tool.projectRevision(revision)
			paths <- projected
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(paths)
	close(errs)
	want := filepath.Join(session.tempVisible, "stella-skills", revision.Skill.Scope, revision.Skill.ID, digest)
	for projected := range paths {
		if projected != want {
			t.Errorf("projection path = %q, want %q", projected, want)
		}
	}
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent projection: %v", err)
		}
	}
	host := filepath.Join(session.tempHost, "stella-skills", revision.Skill.Scope, revision.Skill.ID, digest)
	if content, err := os.ReadFile(filepath.Join(host, "scripts", "support.sh")); err != nil || string(content) != "#!/bin/sh\nprintf complete" {
		t.Fatalf("published projection = %q, %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Dir(host))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != digest {
		t.Fatalf("projection catalog = %#v", entries)
	}
}

func TestManagedLoadRetiresOldDigestAndExcludesDisabledOrPolicyDeniedSkills(t *testing.T) {
	oldDigest, newDigest := strings.Repeat("b", 64), strings.Repeat("c", 64)
	current := projectionSkill("current-id", "current", "")
	disabled := projectionSkill("disabled-id", "disabled", "")
	policyDenied := projectionSkill("policy-id", "policy", "")
	policyDenied.Scope = "system_agent"
	policyDenied.UserID = ""
	reader := &projectionReader{
		identities: []Skill{current, disabled, policyDenied},
		revisions: map[string]ManagedRevision{
			current.ID: {Skill: projectionSkill(current.ID, current.Name, oldDigest), Files: map[string][]byte{MainFile: []byte("old-current")}},
			disabled.ID: {Skill: func() Skill {
				s := projectionSkill(disabled.ID, disabled.Name, strings.Repeat("d", 64))
				s.DisableModelInvocation = true
				return s
			}(), Files: map[string][]byte{MainFile: []byte("must-not-project")}},
			policyDenied.ID: {Skill: func() Skill { s := policyDenied; s.ContentDigest = strings.Repeat("e", 64); return s }(), Files: map[string][]byte{MainFile: []byte("must-not-project-policy")}},
		},
	}
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	tool := NewTool(nil, "", "").WithManagedRevisions(reader, session).
		WithReadAuthorizer(selectedSkillReads{}).WithAgentSkillPolicy([]string{"system_agent:policy"})
	if _, err := tool.load(t.Context(), map[string]any{"name": current.Name}); err != nil {
		t.Fatal(err)
	}
	oldHost := filepath.Join(session.tempHost, "stella-skills", current.Scope, current.ID, oldDigest)
	if _, err := os.Stat(oldHost); err != nil {
		t.Fatal(err)
	}
	reader.revisions[current.ID] = ManagedRevision{Skill: projectionSkill(current.ID, current.Name, newDigest), Files: map[string][]byte{MainFile: []byte("new-current")}}
	if _, err := tool.load(t.Context(), map[string]any{"name": current.Name}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldHost); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("historical projection remained traversable after exact update: %v", err)
	}
	if _, err := tool.load(t.Context(), map[string]any{"name": disabled.Name}); !errors.Is(err, errSkillNotFound) {
		t.Fatalf("disable_model_invocation load = %v", err)
	}
	if _, err := tool.load(t.Context(), map[string]any{"name": policyDenied.Name}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("policy-denied load = %v", err)
	}
	if err := filepath.WalkDir(session.tempHost, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			content, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "must-not-project") {
				t.Errorf("excluded Skill content projected at %q", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
