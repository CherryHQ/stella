package policy

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func evalBuiltins(t *testing.T, authority authz.Authority, request authz.Request) authz.Decision {
	t.Helper()
	evaluation, err := New().Begin(t.Context(), authority)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	decision, err := evaluation.Decide(request)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return decision
}

func TestBuiltinAgentDecisions(t *testing.T) {
	user := userAuthority(t, "u1", false)
	admin := userAuthority(t, "admin1", true)

	if !evalBuiltins(t, user, mustAgentRead(t, "system", "", "system", false)).Allowed() {
		t.Error("user should read a system agent")
	}
	if !evalBuiltins(t, user, mustAgentRead(t, "assigned", "", "user", true)).Allowed() {
		t.Error("user should read an assigned agent")
	}
	if evalBuiltins(t, user, mustAgentRead(t, "unassigned", "", "user", false)).Allowed() {
		t.Error("user should not read an unassigned user-scoped agent")
	}
	if !evalBuiltins(t, admin, mustAgentRead(t, "unassigned", "", "user", false)).Allowed() {
		t.Error("admin should read any agent")
	}
}

func TestDefaultDenyIsForbidden(t *testing.T) {
	request, err := authz.NewRequest(authz.ActionRead, mustResource(t, authz.ResourceSession, "s1", "u1"), authz.InvocationFacts{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	decision := evalBuiltins(t, userAuthority(t, "u1", false), request)
	if decision.Allowed() {
		t.Fatal("session read without ownership facts must default-deny")
	}
	if decision.Visibility() != authz.VisibilityForbidden {
		t.Fatalf("default-deny visibility = %v, want forbidden", decision.Visibility())
	}
}

func TestInvalidRequestFailsClosedAndStaysHidden(t *testing.T) {
	evaluation, err := New().Begin(t.Context(), userAuthority(t, "u1", false))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	decision, err := evaluation.Decide(authz.Request{})
	if !errors.Is(err, authz.ErrInvalidRequest) {
		t.Fatalf("invalid request error = %v, want ErrInvalidRequest", err)
	}
	if decision.Allowed() || decision.Visibility() != authz.VisibilityHidden {
		t.Fatalf("invalid request decision = %+v, want hidden deny", decision)
	}
}

func mustResource(t *testing.T, typ authz.ResourceType, id, owner string) authz.Resource {
	t.Helper()
	resource, err := authz.NewResource(typ, id, owner)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	return resource
}
