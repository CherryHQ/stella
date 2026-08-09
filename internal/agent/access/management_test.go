package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

// fakeAgents backs both the read-only PEP ports (GetAgent/ListAgents) and the
// Management write ports (Create/Update/Delete), tracking mutations so tests can
// assert compensation and reload behavior.
type fakeAgents struct {
	agents                          map[string]config.Agent
	createErr, updateErr, deleteErr error
	deleted                         []string
}

func newFakeAgents(seed ...config.Agent) *fakeAgents {
	m := map[string]config.Agent{}
	for _, a := range seed {
		m[a.ID] = a
	}
	return &fakeAgents{agents: m}
}

func (f *fakeAgents) GetAgent(_ context.Context, id string) (config.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return config.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

func (f *fakeAgents) ListAgents(context.Context) ([]config.Agent, error) {
	out := make([]config.Agent, 0, len(f.agents))
	for _, a := range f.agents {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeAgents) CreateAgent(_ context.Context, a config.Agent) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.agents[a.ID] = a
	return nil
}

func (f *fakeAgents) UpdateAgent(_ context.Context, a config.Agent) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.agents[a.ID] = a
	return nil
}

func (f *fakeAgents) DeleteAgent(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.agents, id)
	return nil
}

type fakeAssign struct {
	byUser                          map[string][]string
	byAgent                         map[string][]string
	assignErr, removeErr, listErr   error
	assignCalls, removeCalls, lists int
}

func newFakeAssign() *fakeAssign {
	return &fakeAssign{byUser: map[string][]string{}, byAgent: map[string][]string{}}
}

func (f *fakeAssign) ListUserAgentIDs(_ context.Context, userID string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byUser[userID], nil
}

func (f *fakeAssign) ListAgentUserIDs(_ context.Context, agentID string) ([]string, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byAgent[agentID], nil
}

func (f *fakeAssign) AssignAgent(_ context.Context, userID, agentID string) error {
	f.assignCalls++
	if f.assignErr != nil {
		return f.assignErr
	}
	f.byUser[userID] = append(f.byUser[userID], agentID)
	f.byAgent[agentID] = append(f.byAgent[agentID], userID)
	return nil
}

func (f *fakeAssign) RemoveAgent(_ context.Context, userID, agentID string) error {
	f.removeCalls++
	return f.removeErr
}

type fakeReloader struct {
	synced []string
	err    error
}

func (f *fakeReloader) SyncAgent(_ context.Context, id string) error {
	f.synced = append(f.synced, id)
	return f.err
}

type fakeUsers struct {
	emails map[string]string
}

func (f fakeUsers) LookupUser(_ context.Context, id string) (UserRef, error) {
	email, ok := f.emails[id]
	if !ok {
		return UserRef{}, errors.New("no such user")
	}
	return UserRef{ID: id, Email: email}, nil
}

func (f fakeUsers) LookupUsers(_ context.Context, ids []string) ([]UserRef, error) {
	out := make([]UserRef, 0, len(ids))
	for _, id := range ids {
		if email, ok := f.emails[id]; ok {
			out = append(out, UserRef{ID: id, Email: email})
		}
	}
	return out, nil
}

type fakeActivity struct {
	m   map[string]time.Time
	err error
}

func (f fakeActivity) ListAgentLastActive(context.Context, string) (map[string]time.Time, error) {
	return f.m, f.err
}

func newManagement(agents *fakeAgents, assign *fakeAssign, reloader AgentReloader, users UserDirectory, activity ActivityReader) *Management {
	pep := NewService(agents, assign)
	return NewManagement(pep, agents, assign, reloader, users, activity, nil, nil, nil)
}

func TestManagementCreateNonAdminRestrictedAndAutoAssigns(t *testing.T) {
	agents := newFakeAgents()
	assign := newFakeAssign()
	reloader := &fakeReloader{}
	m := newManagement(agents, assign, reloader, fakeUsers{}, fakeActivity{})

	got, err := m.Create(context.Background(), userAuthority(t, "u1", false), config.Agent{ID: "foo", Name: "Foo", Workspace: "/hack"})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if got.Scope != config.AgentScopeRestricted {
		t.Fatalf("scope = %q, want restricted (non-admin default)", got.Scope)
	}
	if got.CreatorID != "u1" {
		t.Fatalf("creator = %q, want u1", got.CreatorID)
	}
	if got.Workspace != "" {
		t.Fatalf("workspace = %q, want cleared", got.Workspace)
	}
	if assign.assignCalls != 1 || len(assign.byAgent["foo"]) != 1 {
		t.Fatalf("expected creator auto-assigned, calls=%d", assign.assignCalls)
	}
	if len(reloader.synced) != 1 || reloader.synced[0] != "foo" {
		t.Fatalf("expected reload of foo, got %v", reloader.synced)
	}
}

func TestManagementCreateDeduplicatesID(t *testing.T) {
	agents := newFakeAgents(config.Agent{ID: "foo", Scope: config.AgentScopeSystem})
	m := newManagement(agents, newFakeAssign(), &fakeReloader{}, fakeUsers{}, fakeActivity{})
	got, err := m.Create(context.Background(), userAuthority(t, "admin", true), config.Agent{ID: "foo", Name: "Foo", Scope: config.AgentScopeSystem})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if got.ID != "foo-2" {
		t.Fatalf("id = %q, want foo-2", got.ID)
	}
}

func TestManagementCreateAdminScope(t *testing.T) {
	// Empty scope defaults to system for an admin, with no auto-assignment.
	assign := newFakeAssign()
	m := newManagement(newFakeAgents(), assign, &fakeReloader{}, fakeUsers{}, fakeActivity{})
	got, err := m.Create(context.Background(), userAuthority(t, "admin", true), config.Agent{ID: "a", Name: "A"})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if got.Scope != config.AgentScopeSystem {
		t.Fatalf("scope = %q, want system", got.Scope)
	}
	if assign.assignCalls != 0 {
		t.Fatalf("admin create must not auto-assign, calls=%d", assign.assignCalls)
	}
	// A bogus admin scope is a validation error.
	if _, err := m.Create(context.Background(), userAuthority(t, "admin", true), config.Agent{ID: "b", Name: "B", Scope: "bogus"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("bad scope = %v, want ErrInvalidScope", err)
	}
}

func TestManagementCreateNonAdminMayOpenToEveryone(t *testing.T) {
	// Opening an agent to everyone names no other user, so an ordinary creator
	// may ask for it; no assignment row is needed for a system agent.
	assign := newFakeAssign()
	m := newManagement(newFakeAgents(), assign, &fakeReloader{}, fakeUsers{}, fakeActivity{})
	got, err := m.Create(context.Background(), userAuthority(t, "u1", false), config.Agent{ID: "a", Name: "A", Scope: config.AgentScopeSystem})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if got.Scope != config.AgentScopeSystem {
		t.Fatalf("scope = %q, want system", got.Scope)
	}
	if assign.assignCalls != 0 {
		t.Fatalf("system create must not auto-assign, calls=%d", assign.assignCalls)
	}
	if _, err := m.Create(context.Background(), userAuthority(t, "u1", false), config.Agent{ID: "b", Name: "B", Scope: "bogus"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("bad scope = %v, want ErrInvalidScope", err)
	}
}

func TestManagementCreateCompensatesFailedAutoAssign(t *testing.T) {
	agents := newFakeAgents()
	assign := newFakeAssign()
	assign.assignErr = errors.New("assign down")
	reloader := &fakeReloader{}
	m := newManagement(agents, assign, reloader, fakeUsers{}, fakeActivity{})

	_, err := m.Create(context.Background(), userAuthority(t, "u1", false), config.Agent{ID: "foo", Name: "Foo"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("create = %v, want ErrUnavailable", err)
	}
	if _, ok := agents.agents["foo"]; ok {
		t.Fatal("agent must be rolled back after failed auto-assign, but it persists")
	}
	if len(agents.deleted) != 1 || agents.deleted[0] != "foo" {
		t.Fatalf("expected compensating delete of foo, got %v", agents.deleted)
	}
	if len(reloader.synced) != 0 {
		t.Fatalf("failed auto-assign must not reach the success reload, got %v", reloader.synced)
	}
}

func TestManagementCreateReloadFailureIsBestEffort(t *testing.T) {
	agents := newFakeAgents()
	reloader := &fakeReloader{err: errors.New("pool down")}
	m := newManagement(agents, newFakeAssign(), reloader, fakeUsers{}, fakeActivity{})
	got, err := m.Create(context.Background(), userAuthority(t, "admin", true), config.Agent{ID: "foo", Name: "Foo"})
	if err != nil {
		t.Fatalf("create = %v, want nil despite reload failure", err)
	}
	if _, ok := agents.agents[got.ID]; !ok {
		t.Fatal("agent must persist despite reload failure")
	}
}

func TestManagementCreateDeniesInvalidAuthority(t *testing.T) {
	m := newManagement(newFakeAgents(), newFakeAssign(), &fakeReloader{}, fakeUsers{}, fakeActivity{})
	if _, err := m.Create(context.Background(), authz.Authority{}, config.Agent{ID: "foo", Name: "Foo"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create = %v, want ErrForbidden", err)
	}
}

func TestManagementUpdateScopeRules(t *testing.T) {
	ctx := context.Background()
	seed := config.Agent{ID: "a", Scope: config.AgentScopeRestricted, CreatorID: "u1", Enabled: true}

	// The creator opens their own agent to every user, and cannot spoof the
	// server-owned workspace or creator while doing it.
	agents := newFakeAgents(seed)
	m := newManagement(agents, newFakeAssign(), &fakeReloader{}, fakeUsers{}, fakeActivity{})
	got, err := m.Update(ctx, userAuthority(t, "u1", false), config.Agent{ID: "a", Name: "A", Scope: config.AgentScopeSystem, Workspace: "/caller/path", CreatorID: "spoofed"})
	if err != nil {
		t.Fatalf("update = %v", err)
	}
	if got.Scope != config.AgentScopeSystem {
		t.Fatalf("scope = %q, want system", got.Scope)
	}
	if got.Workspace != "" {
		t.Fatalf("workspace = %q, want canonical default", got.Workspace)
	}
	if got.CreatorID != "u1" {
		t.Fatalf("creator = %q, want persisted creator u1", got.CreatorID)
	}

	// An omitted scope keeps the persisted one for a creator.
	got, err = m.Update(ctx, userAuthority(t, "u1", false), config.Agent{ID: "a", Name: "A"})
	if err != nil {
		t.Fatalf("update without scope = %v", err)
	}
	if got.Scope != config.AgentScopeSystem {
		t.Fatalf("scope = %q, want persisted system", got.Scope)
	}

	// Narrowing back to restricted re-assigns the creator, so the manager of a
	// system-created agent does not lose access to it.
	assign := newFakeAssign()
	narrowing := newManagement(newFakeAgents(config.Agent{ID: "b", Scope: config.AgentScopeSystem, CreatorID: "u1", Enabled: true}), assign, &fakeReloader{}, fakeUsers{}, fakeActivity{})
	if _, err := narrowing.Update(ctx, userAuthority(t, "u1", false), config.Agent{ID: "b", Name: "B", Scope: config.AgentScopeRestricted}); err != nil {
		t.Fatalf("narrow to restricted = %v", err)
	}
	if assign.assignCalls != 1 {
		t.Fatalf("assign calls = %d, want 1 (creator kept access)", assign.assignCalls)
	}

	// A creator supplying a bogus scope is a validation error, same as an admin.
	if _, err := m.Update(ctx, userAuthority(t, "u1", false), config.Agent{ID: "a", Name: "A", Scope: "bogus"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("bad creator scope = %v, want ErrInvalidScope", err)
	}

	// A non-creator, non-admin is denied by the Manage decision.
	if _, err := m.Update(ctx, userAuthority(t, "u2", false), config.Agent{ID: "a", Name: "A"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-creator update = %v, want ErrForbidden", err)
	}

	// An admin supplying a bogus scope is a validation error.
	if _, err := m.Update(ctx, userAuthority(t, "admin", true), config.Agent{ID: "a", Name: "A", Scope: "bogus"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("bad admin scope = %v, want ErrInvalidScope", err)
	}
}

func TestManagementDelete(t *testing.T) {
	ctx := context.Background()
	seed := config.Agent{ID: "a", Scope: config.AgentScopeRestricted, CreatorID: "u1", Enabled: true}

	agents := newFakeAgents(seed)
	reloader := &fakeReloader{}
	m := newManagement(agents, newFakeAssign(), reloader, fakeUsers{}, fakeActivity{})
	if err := m.Delete(ctx, userAuthority(t, "u1", false), "a"); err != nil {
		t.Fatalf("delete = %v", err)
	}
	if _, ok := agents.agents["a"]; ok {
		t.Fatal("agent should be deleted")
	}
	if len(reloader.synced) != 1 {
		t.Fatalf("expected reload after delete, got %v", reloader.synced)
	}

	// A non-creator is denied.
	agents2 := newFakeAgents(seed)
	m2 := newManagement(agents2, newFakeAssign(), &fakeReloader{}, fakeUsers{}, fakeActivity{})
	if err := m2.Delete(ctx, userAuthority(t, "u2", false), "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-creator delete = %v, want ErrForbidden", err)
	}
}

func TestManagementAssignUser(t *testing.T) {
	ctx := context.Background()
	agents := newFakeAgents(config.Agent{ID: "a", Scope: config.AgentScopeRestricted})
	assign := newFakeAssign()
	users := fakeUsers{emails: map[string]string{"u1": "u1@example.com"}}
	m := newManagement(agents, assign, &fakeReloader{}, users, fakeActivity{})

	// Non-admin is denied before any durable read.
	if _, err := m.AssignUser(ctx, userAuthority(t, "u1", false), "a", "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin assign = %v, want ErrForbidden", err)
	}

	admin := userAuthority(t, "admin", true)
	// Missing agent -> ErrNotFound (checked before the user).
	if _, err := m.AssignUser(ctx, admin, "missing", "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent = %v, want ErrNotFound", err)
	}
	// Missing user -> ErrUserNotFound.
	if _, err := m.AssignUser(ctx, admin, "a", "ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user = %v, want ErrUserNotFound", err)
	}
	// Success returns the resolved reference and records the assignment.
	ref, err := m.AssignUser(ctx, admin, "a", "u1")
	if err != nil {
		t.Fatalf("assign = %v", err)
	}
	if ref.ID != "u1" || ref.Email != "u1@example.com" {
		t.Fatalf("ref = %#v", ref)
	}
	if assign.assignCalls != 1 {
		t.Fatalf("assign calls = %d, want 1", assign.assignCalls)
	}
}

func TestManagementRemoveUser(t *testing.T) {
	ctx := context.Background()
	assign := newFakeAssign()
	m := newManagement(newFakeAgents(), assign, &fakeReloader{}, fakeUsers{}, fakeActivity{})
	if err := m.RemoveUser(ctx, userAuthority(t, "u1", false), "a", "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin remove = %v, want ErrForbidden", err)
	}
	if assign.removeCalls != 0 {
		t.Fatalf("denied remove must not touch the store, calls=%d", assign.removeCalls)
	}
	if err := m.RemoveUser(ctx, userAuthority(t, "admin", true), "a", "u1"); err != nil {
		t.Fatalf("admin remove = %v", err)
	}
	if assign.removeCalls != 1 {
		t.Fatalf("remove calls = %d, want 1", assign.removeCalls)
	}
}

func TestManagementListAssignedUsersSkipsStaleLinks(t *testing.T) {
	ctx := context.Background()
	assign := newFakeAssign()
	assign.byAgent["a"] = []string{"u1", "ghost", "u2"}
	users := fakeUsers{emails: map[string]string{"u1": "u1@x", "u2": "u2@x"}}
	m := newManagement(newFakeAgents(), assign, &fakeReloader{}, users, fakeActivity{})

	if _, err := m.ListAssignedUsers(ctx, userAuthority(t, "u1", false), "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin list = %v, want ErrForbidden", err)
	}
	got, err := m.ListAssignedUsers(ctx, userAuthority(t, "admin", true), "a")
	if err != nil {
		t.Fatalf("list = %v", err)
	}
	if len(got) != 2 || got[0].ID != "u1" || got[1].ID != "u2" {
		t.Fatalf("assigned users = %#v, want u1,u2 in order (stale ghost skipped)", got)
	}
}

func TestManagementListAgentLastActive(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	m := newManagement(newFakeAgents(), newFakeAssign(), &fakeReloader{}, fakeUsers{}, fakeActivity{m: map[string]time.Time{"a": now}})
	got, err := m.ListAgentLastActive(ctx, "u1")
	if err != nil {
		t.Fatalf("last active = %v", err)
	}
	if !got["a"].Equal(now) {
		t.Fatalf("last active[a] = %v, want %v", got["a"], now)
	}

	// A nil activity reader degrades to an empty map, never a nil-deref.
	mNil := NewManagement(NewService(newFakeAgents(), newFakeAssign()), newFakeAgents(), newFakeAssign(), &fakeReloader{}, fakeUsers{}, nil, nil, nil, nil)
	empty, err := mNil.ListAgentLastActive(ctx, "u1")
	if err != nil || len(empty) != 0 {
		t.Fatalf("nil activity = (%v, %v), want (empty, nil)", empty, err)
	}
}
