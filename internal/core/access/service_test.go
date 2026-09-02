package access

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
)

type testStore struct {
	agents                 map[string]config.Agent
	channels               map[string]config.Channel
	listErr, getErr, chErr error
}

func (s testStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	if s.getErr != nil {
		return config.Agent{}, s.getErr
	}
	a, ok := s.agents[id]
	if !ok {
		return config.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

func (s testStore) ListAgents(context.Context) ([]config.Agent, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]config.Agent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	return out, nil
}

func (s testStore) GetChannel(_ context.Context, id string) (config.Channel, error) {
	if s.chErr != nil {
		return config.Channel{}, s.chErr
	}
	channel, ok := s.channels[id]
	if !ok {
		return config.Channel{}, pgx.ErrNoRows
	}
	return channel, nil
}

type testAssignments struct {
	ids   []string
	err   error
	calls int
}

func (s *testAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	s.calls++
	return s.ids, s.err
}

func userAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(userID), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func TestListReadableFiltersEachRowAndCachesAssignments(t *testing.T) {
	store := testStore{agents: map[string]config.Agent{
		"allow": {ID: "allow", Scope: config.AgentScopeSystem, CreatorID: "creator", Enabled: true},
		"deny":  {ID: "deny", Scope: config.AgentScopeRestricted, CreatorID: "creator", Enabled: true},
	}}
	assign := &testAssignments{}
	got, err := NewService(store, assign).ListReadable(context.Background(), userAuthority(t, "u1", false), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "allow" {
		t.Fatalf("visible = %#v", got)
	}
	if assign.calls != 1 {
		t.Fatalf("assignment calls = %d, want 1", assign.calls)
	}
}

// Disabling your own agent must not hide it: the list is how the UI reaches an
// agent's configuration, so the creator would lose the switch that turns it
// back on. Everyone else still sees nothing.
func TestListReadableKeepsADisabledAgentVisibleToItsCreator(t *testing.T) {
	store := testStore{agents: map[string]config.Agent{
		"mine":  {ID: "mine", Scope: config.AgentScopeSystem, CreatorID: "u1", Enabled: false},
		"other": {ID: "other", Scope: config.AgentScopeSystem, CreatorID: "u2", Enabled: false},
		"live":  {ID: "live", Scope: config.AgentScopeSystem, CreatorID: "u2", Enabled: true},
	}}
	svc := NewService(store, &testAssignments{})

	got, err := svc.ListReadable(context.Background(), userAuthority(t, "u1", false), false)
	if err != nil {
		t.Fatal(err)
	}
	if ids := agentIDs(got); len(ids) != 2 || !ids["mine"] || !ids["live"] {
		t.Fatalf("creator sees %v, want mine and live", ids)
	}

	// Someone else's disabled agent still takes the deliberate include_all rather
	// than arriving unasked, admin or not. "live" is scoped to everyone, so it is
	// in every user's fleet.
	got, err = svc.ListReadable(context.Background(), userAuthority(t, "root", true), false)
	if err != nil {
		t.Fatal(err)
	}
	if ids := agentIDs(got); len(ids) != 1 || !ids["live"] {
		t.Fatalf("admin sees %v, want live only", ids)
	}

	// An admin is a creator like anyone else. The page holding the enable switch
	// does not ask for include_all, so excluding admins here would lock them out
	// of their own agent exactly the way this test's first case describes.
	got, err = svc.ListReadable(context.Background(), userAuthority(t, "u2", true), false)
	if err != nil {
		t.Fatal(err)
	}
	if ids := agentIDs(got); !ids["other"] {
		t.Fatalf("admin creator sees %v, want their own disabled agent", ids)
	}
	got, err = svc.ListReadable(context.Background(), userAuthority(t, "root", true), true)
	if err != nil {
		t.Fatal(err)
	}
	if ids := agentIDs(got); len(ids) != 3 {
		t.Fatalf("admin with include_all sees %v, want all three", ids)
	}
}

// Being able to reach every agent is not a reason to be shown every agent. The
// default list is the caller's own fleet even on an admin account, so /agents
// stays a workspace instead of a directory of everybody's agents.
func TestListReadableGivesAnAdminTheirOwnFleetByDefault(t *testing.T) {
	store := testStore{agents: map[string]config.Agent{
		"mine":       {ID: "mine", Scope: config.AgentScopeRestricted, CreatorID: "root", Enabled: true},
		"assigned":   {ID: "assigned", Scope: config.AgentScopeRestricted, CreatorID: "u2", Enabled: true},
		"restricted": {ID: "restricted", Scope: config.AgentScopeRestricted, CreatorID: "u2", Enabled: true},
		"everyone":   {ID: "everyone", Scope: config.AgentScopeSystem, CreatorID: "u2", Enabled: true},
	}}
	svc := NewService(store, &testAssignments{ids: []string{"assigned"}})

	got, err := svc.ListReadable(context.Background(), userAuthority(t, "root", true), false)
	if err != nil {
		t.Fatal(err)
	}
	ids := agentIDs(got)
	if len(ids) != 3 || !ids["mine"] || !ids["assigned"] || !ids["everyone"] {
		t.Fatalf("admin sees %v, want their own, their assigned, and the shared agent", ids)
	}
	if ids["restricted"] {
		t.Error("admin was handed another user's restricted agent unasked")
	}

	// The deployment-wide view is still there for the admin who asks for it.
	got, err = svc.ListReadable(context.Background(), userAuthority(t, "root", true), true)
	if err != nil {
		t.Fatal(err)
	}
	if ids := agentIDs(got); len(ids) != 4 {
		t.Fatalf("admin with include_all sees %v, want all four", ids)
	}
}

func agentIDs(agents []config.Agent) map[string]bool {
	out := make(map[string]bool, len(agents))
	for _, a := range agents {
		out[a.ID] = true
	}
	return out
}

func TestFailuresFailClosed(t *testing.T) {
	ctx := context.Background()
	user := userAuthority(t, "u1", false)
	if _, err := NewService(testStore{getErr: errors.New("db")}, &testAssignments{}).Read(ctx, user, "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get error = %v", err)
	}
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: "bad"}}}, &testAssignments{}).Read(ctx, user, "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad scope = %v", err)
	}
	// An ordinary user reading a non-system agent needs the assignment lookup, so a
	// failing store fails closed.
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: config.AgentScopeRestricted}}}, &testAssignments{err: errors.New("db")}).Read(ctx, user, "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("assignment error = %v", err)
	}
	if err := NewService(testStore{}, &testAssignments{}).CanList(ctx, authz.Authority{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid authority = %v", err)
	}
}

// TestNoNeedlessAssignmentQuery proves the decision paths that do not depend on a
// user's agent assignments never touch the AssignmentStore: admin, a system
// actor, and an ordinary user reading a system-scope agent all succeed through a
// store that would error the moment it were consulted, and none of them consult
// it. This removes the former admin/availability coupling to the assignment query
// without weakening any access.
func TestNoNeedlessAssignmentQuery(t *testing.T) {
	ctx := context.Background()
	store := testStore{agents: map[string]config.Agent{
		"sys":        {ID: "sys", Scope: config.AgentScopeSystem, Enabled: true},
		"restricted": {ID: "restricted", Scope: config.AgentScopeRestricted, CreatorID: "creator", Enabled: true},
	}}

	admin := userAuthority(t, "admin", true)
	adminAssign := &testAssignments{err: errors.New("db")}
	if _, err := NewService(store, adminAssign).Read(ctx, admin, "restricted"); err != nil {
		t.Fatalf("admin read = %v, want nil despite failing assignment store", err)
	}
	if adminAssign.calls != 0 {
		t.Fatalf("admin touched the assignment store %d times, want 0", adminAssign.calls)
	}

	system, err := SystemAgentAuthority("test")
	if err != nil {
		t.Fatal(err)
	}
	sysAssign := &testAssignments{err: errors.New("db")}
	if _, err := NewService(store, sysAssign).Read(ctx, system, "restricted"); err != nil {
		t.Fatalf("system read = %v, want nil despite failing assignment store", err)
	}
	if sysAssign.calls != 0 {
		t.Fatalf("system touched the assignment store %d times, want 0", sysAssign.calls)
	}

	user := userAuthority(t, "u1", false)
	userAssign := &testAssignments{err: errors.New("db")}
	if _, err := NewService(store, userAssign).Read(ctx, user, "sys"); err != nil {
		t.Fatalf("user system-scope read = %v, want nil despite failing assignment store", err)
	}
	if userAssign.calls != 0 {
		t.Fatalf("user system-scope read touched the assignment store %d times, want 0", userAssign.calls)
	}
}
