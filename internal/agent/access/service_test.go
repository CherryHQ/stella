package access

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
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

func userAuthority(t *testing.T, userID string, roles ...authz.Role) authz.Authority {
	t.Helper()
	roleSet, err := authz.NewRoleSet(roles...)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(userID), roleSet, authz.GrantSet{})
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
	got, err := NewService(store, assign).ListReadable(context.Background(), userAuthority(t, "u1", authz.RoleUser), false)
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

func TestFailuresFailClosed(t *testing.T) {
	ctx := context.Background()
	user := userAuthority(t, "u1", authz.RoleUser)
	if _, err := NewService(testStore{getErr: errors.New("db")}, &testAssignments{}).Read(ctx, user, "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get error = %v", err)
	}
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: "bad"}}}, &testAssignments{}).Read(ctx, user, "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad scope = %v", err)
	}
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: config.AgentScopeSystem}}}, &testAssignments{err: errors.New("db")}).Read(ctx, user, "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("assignment error = %v", err)
	}
	if err := NewService(testStore{}, &testAssignments{}).CanList(ctx, authz.Authority{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid authority = %v", err)
	}
}
