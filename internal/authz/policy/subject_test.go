package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// mustGrant unwraps a grant constructor's (Grant, error) result. It takes the
// pair directly so it can be called as mustGrant(authz.SystemGrant("x")). The
// grant keys used here are static and valid, so an error is a test bug.
func mustGrant(g authz.Grant, err error) authz.Grant {
	if err != nil {
		panic("authz/policy test: build grant: " + err.Error())
	}
	return g
}

func groupAgentAuthority(t *testing.T, group, agent string, gs ...authz.Grant) authz.Authority {
	t.Helper()
	set, err := authz.NewGrantSet(gs...)
	if err != nil {
		t.Fatalf("grant set: %v", err)
	}
	a, err := authz.NewGroupAgentAuthority(authz.GroupID(group), authz.AgentID(agent), authz.RoleSet{}, set)
	if err != nil {
		t.Fatalf("group-agent authority: %v", err)
	}
	return a
}

func systemAuthority(t *testing.T, name string, gs ...authz.Grant) authz.Authority {
	t.Helper()
	set, err := authz.NewGrantSet(gs...)
	if err != nil {
		t.Fatalf("grant set: %v", err)
	}
	a, err := authz.NewSystemAuthority(authz.Component(name), set)
	if err != nil {
		t.Fatalf("system authority: %v", err)
	}
	return a
}

func userWithGrants(t *testing.T, userID string, gs ...authz.Grant) authz.Authority {
	t.Helper()
	set, err := authz.NewGrantSet(gs...)
	if err != nil {
		t.Fatalf("grant set: %v", err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(userID), authz.RoleSet{}, set)
	if err != nil {
		t.Fatalf("user authority: %v", err)
	}
	return a
}

func TestSelectorValidation(t *testing.T) {
	if err := AnySubject().validate(); err != nil {
		t.Fatalf("AnySubject must be valid: %v", err)
	}
	if err := (Selector{}).validate(); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("zero selector: got %v, want ErrInvalidSelector", err)
	}
	// Any must not also carry dimensions.
	if err := (Selector{matchAny: true, roles: []authz.Role{authz.RoleUser}}).validate(); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("any+dims: got %v, want ErrInvalidSelector", err)
	}
	if err := (Selector{kinds: []authz.ActorKind{authz.ActorKind(200)}}).validate(); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("unknown kind: got %v, want ErrInvalidSelector", err)
	}
	if err := (Selector{roles: []authz.Role{authz.Role(200)}}).validate(); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("unknown role: got %v, want ErrInvalidSelector", err)
	}
	if err := (Selector{grants: []authz.Grant{{}}}).validate(); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("zero grant: got %v, want ErrInvalidSelector", err)
	}
	if err := NewSubjectBuilder().Roles(authz.RoleUser).Build().validate(); err != nil {
		t.Fatalf("role selector must be valid: %v", err)
	}
}

func TestSelectorMatching(t *testing.T) {
	user := userAuthority(t, "u1", false)
	admin := userAuthority(t, "admin1", true)
	groupAgent := groupAgentAuthority(t, "g1", "a1")

	// GroupAgent cannot match a user-role selector (it carries no role).
	if NewSubjectBuilder().Roles(authz.RoleUser).Build().matches(groupAgent) {
		t.Error("group-agent must not match a user-role selector")
	}
	if !NewSubjectBuilder().Roles(authz.RoleUser).Build().matches(user) {
		t.Error("user must match a user-role selector")
	}

	// Kind dimension.
	if !NewSubjectBuilder().Kinds(authz.ActorUser).Build().matches(user) {
		t.Error("user must match ActorUser kind")
	}
	if NewSubjectBuilder().Kinds(authz.ActorUser).Build().matches(groupAgent) {
		t.Error("group-agent must not match ActorUser kind")
	}

	// AND across dimensions: user-kind AND admin-role.
	kindAndRole := NewSubjectBuilder().Kinds(authz.ActorUser).Roles(authz.RoleAdmin).Build()
	if kindAndRole.matches(user) {
		t.Error("plain user must not match (user-kind AND admin-role)")
	}
	if !kindAndRole.matches(admin) {
		t.Error("admin must match (user-kind AND admin-role)")
	}

	// OR within a dimension.
	eitherRole := NewSubjectBuilder().Roles(authz.RoleUser, authz.RoleAdmin).Build()
	if !eitherRole.matches(user) || !eitherRole.matches(admin) {
		t.Error("either-role selector must match both user and admin")
	}

	// Explicit Any matches everyone.
	for _, a := range []authz.Authority{user, admin, groupAgent} {
		if !AnySubject().matches(a) {
			t.Errorf("AnySubject must match %s", a.Kind())
		}
	}

	// Zero selector matches nothing (fail closed).
	if (Selector{}).matches(user) {
		t.Error("zero selector must match nothing")
	}
}

func TestSystemActorOnlyMatchesExactNamedGrant(t *testing.T) {
	maint := mustGrant(authz.SystemGrant("backfill"))
	sel := NewSubjectBuilder().Grants(maint).Build()

	withGrant := systemAuthority(t, "worker", maint)
	if !sel.matches(withGrant) {
		t.Error("system actor with the exact named grant must match")
	}
	otherGrant := systemAuthority(t, "worker", mustGrant(authz.SystemGrant("other")))
	if sel.matches(otherGrant) {
		t.Error("system actor with a different named grant must not match")
	}
	// A user actor never holds a system grant, so never matches.
	if sel.matches(userAuthority(t, "u1", false)) {
		t.Error("user actor must not match a system-grant selector")
	}
}

func TestExactGrantsDoNotCross(t *testing.T) {
	chan1 := mustGrant(authz.ChannelBindingGrant("chan1"))
	sel := NewSubjectBuilder().Grants(chan1).Build()

	if !sel.matches(userWithGrants(t, "u1", chan1)) {
		t.Error("exact channel-binding grant must match itself")
	}
	// Different key -> no match.
	if sel.matches(userWithGrants(t, "u1", mustGrant(authz.ChannelBindingGrant("chan2")))) {
		t.Error("a different channel binding must not cross")
	}
	// Same key, different kind -> no match.
	if sel.matches(userWithGrants(t, "u1", mustGrant(authz.AgentToolGrant("chan1")))) {
		t.Error("an agent-tool grant with the same key must not cross into a channel binding")
	}
	// No grant -> no match.
	if sel.matches(userAuthority(t, "u1", false)) {
		t.Error("a user with no grants must not match a grant selector")
	}
}

// A custom deny scoped to a user-role selector denies the user but leaves the
// admin allowed (deny-overrides is preserved AND the selector confines the deny).
func TestDenyOverridesRespectsSelectorScope(t *testing.T) {
	user := userAuthority(t, "u1", false)
	admin := userAuthority(t, "admin1", true)
	req := mustAgentRead(t, "a1", "", "system", false)

	deny, err := compileCustom("c:deny-user", "agent", "read", "deny",
		NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		[]predicate{{Attr: "scope", Op: opEq, Value: "system"}})
	if err != nil {
		t.Fatalf("compile deny: %v", err)
	}
	snap := &snapshot{revision: 1, policies: append(builtinPolicies(), deny)}

	if dec, _ := (&evaluation{authority: user, snap: snap}).Decide(req); dec.Allowed() {
		t.Error("user must be denied by the user-scoped deny")
	}
	if dec, _ := (&evaluation{authority: admin, snap: snap}).Decide(req); !dec.Allowed() {
		t.Error("admin must remain allowed: the user-scoped deny does not match admin")
	}
}

// An active row whose persisted subject selector is invalid (unknown role, or
// the empty zero selector) fails the whole Begin — it is never treated as "any
// actor".
func TestMaliciousActiveSubjectRowFailsBegin(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		subjects string
	}{
		{"unknown role", `{"roles":["wizard"]}`},
		{"empty zero selector", `{}`},
		{"unknown grant kind", `{"grants":[{"kind":"telepathy","key":"x"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := dbtest.New(t)
			if _, err := sqlc.New(pool).CreateAuthzPolicy(ctx, sqlc.CreateAuthzPolicyParams{
				ID:             "malicious",
				ResourceType:   authz.ResourceAgent.String(),
				Action:         authz.ActionRead.String(),
				Effect:         string(EffectAllow),
				Subjects:       []byte(tc.subjects),
				Attributes:     []byte(`{}`),
				CatalogVersion: int64(authz.CatalogVersion),
				Status:         statusActive,
			}); err != nil {
				t.Fatalf("seed malicious row: %v", err)
			}
			az := New(pool)
			_, err := az.Begin(ctx, userAuthority(t, "u1", false))
			if !errors.Is(err, authz.ErrAuthorizerUnavailable) {
				t.Fatalf("Begin over invalid active subject row = %v, want ErrAuthorizerUnavailable", err)
			}
		})
	}
}

func TestCreatePolicyRequiresValidSelector(t *testing.T) {
	ctx := context.Background()
	svc := NewService(New(dbtest.New(t)))
	// Zero selector (the default) is rejected before any write.
	_, _, err := svc.CreatePolicy(ctx, PolicyInput{
		Resource: authz.ResourceAgent,
		Action:   authz.ActionRead,
		Effect:   EffectAllow,
		// Subjects left zero on purpose.
	})
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("create without selector = %v, want ErrInvalidSelector", err)
	}
}

// End-to-end: a persisted selector round-trips through reload and confines the
// decision — a group grant selector allows only a group actor holding it.
func TestPersistedSelectorConfinesDecision(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	svc := NewService(az)

	groupTool := mustGrant(authz.GroupToolGrant("summarize"))
	if _, _, err := svc.CreatePolicy(ctx, PolicyInput{
		Name:     "group summarize allow",
		Resource: authz.ResourceAgent,
		Action:   authz.ActionExecute,
		Effect:   EffectAllow,
		Subjects: NewSubjectBuilder().Grants(groupTool).Build(),
	}); err != nil {
		t.Fatalf("create group-scoped policy: %v", err)
	}

	req, err := AgentExecuteRequest("a1", "", "user", false)
	if err != nil {
		t.Fatalf("agent execute request: %v", err)
	}

	// A group-agent holding the exact grant is allowed by the custom policy.
	holder := groupAgentAuthority(t, "g1", "a1", groupTool)
	eval, err := az.Begin(ctx, holder)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if dec, _ := eval.Decide(req); !dec.Allowed() {
		t.Error("group-agent holding the group grant must be allowed by the custom policy")
	}

	// A group-agent WITHOUT the grant falls through to default deny.
	other := groupAgentAuthority(t, "g1", "a1")
	eval2, err := az.Begin(ctx, other)
	if err != nil {
		t.Fatalf("begin other: %v", err)
	}
	if dec, _ := eval2.Decide(req); dec.Allowed() {
		t.Error("group-agent without the grant must not be allowed")
	}
}
