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
	agents          map[string]config.Agent
	listErr, getErr error
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

type testAssignments struct {
	ids   []string
	err   error
	calls int
}

func (s *testAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	s.calls++
	return s.ids, s.err
}

type testAuthorizer struct {
	eval   *testEvaluation
	err    error
	begins int
}

func (a *testAuthorizer) Begin(context.Context, authz.Authority) (authz.Evaluation, error) {
	a.begins++
	if a.err != nil {
		return nil, a.err
	}
	return a.eval, nil
}

type testEvaluation struct {
	decide func(authz.Request) (authz.Decision, error)
	calls  int
}

func (e *testEvaluation) Decide(r authz.Request) (authz.Decision, error) {
	e.calls++
	return e.decide(r)
}
func (*testEvaluation) Revision() int64 { return 7 }
func userAuthority(t *testing.T) authz.Authority {
	t.Helper()
	roles, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority("u1", roles, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestListReadableUsesOneEvaluationAndFiltersEachRead(t *testing.T) {
	store := testStore{agents: map[string]config.Agent{
		"allow": {ID: "allow", Scope: config.AgentScopeSystem, CreatorID: "creator", Enabled: true},
		"deny":  {ID: "deny", Scope: config.AgentScopeRestricted, CreatorID: "creator", Enabled: true},
	}}
	assign := &testAssignments{}
	eval := &testEvaluation{decide: func(r authz.Request) (authz.Decision, error) {
		if r.Action() == authz.ActionList {
			return authz.Allow("", authz.AuditRecord{}), nil
		}
		if r.Resource().ID() == "deny" {
			return authz.Deny(authz.VisibilityHidden, "", authz.AuditRecord{}), nil
		}
		return authz.Allow("", authz.AuditRecord{}), nil
	}}
	az := &testAuthorizer{eval: eval}
	got, err := NewService(store, assign, az).ListReadable(context.Background(), userAuthority(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "allow" {
		t.Fatalf("visible = %#v", got)
	}
	if az.begins != 1 {
		t.Fatalf("Begin calls = %d, want 1", az.begins)
	}
	if assign.calls != 1 {
		t.Fatalf("assignment calls = %d, want 1", assign.calls)
	}
}

func TestAgentRequestsCarryCompleteCanonicalFacts(t *testing.T) {
	store := testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: config.AgentScopeRestricted, CreatorID: "u1", Enabled: false}}}
	var got authz.Request
	eval := &testEvaluation{decide: func(r authz.Request) (authz.Decision, error) {
		got = r
		return authz.Allow("", authz.AuditRecord{}), nil
	}}
	_, err := NewService(store, &testAssignments{ids: []string{"a"}}, &testAuthorizer{eval: eval}).Delete(context.Background(), userAuthority(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"scope": "user", "assigned": "true", "creator": "u1", "is_creator": "true", "is_executor": "false", "dedicated": "false", "status": "disabled"}
	for k, v := range want {
		if gotV, ok := got.Resource().Attr(k); !ok || gotV != v {
			t.Errorf("%s = %q,%t want %q", k, gotV, ok, v)
		}
	}
	if got.Action() != authz.ActionDelete {
		t.Errorf("action = %s", got.Action())
	}
}

func TestFailuresFailClosed(t *testing.T) {
	az := &testAuthorizer{err: errors.New("down")}
	if _, err := NewService(testStore{}, &testAssignments{}, az).Read(context.Background(), userAuthority(t), "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("begin error = %v", err)
	}
	az = &testAuthorizer{eval: &testEvaluation{decide: func(authz.Request) (authz.Decision, error) { return authz.Allow("", authz.AuditRecord{}), nil }}}
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: "bad"}}}, &testAssignments{}, az).Read(context.Background(), userAuthority(t), "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad scope = %v", err)
	}
	if _, err := NewService(testStore{agents: map[string]config.Agent{"a": {ID: "a", Scope: config.AgentScopeSystem}}}, &testAssignments{err: errors.New("db")}, az).Read(context.Background(), userAuthority(t), "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("assignment error = %v", err)
	}
}
