package policy

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// evalWith builds an evaluation over the built-ins plus any extra compiled
// policies, then decides req.
func evalWith(t *testing.T, a authz.Authority, req authz.Request, extra ...compiledPolicy) authz.Decision {
	t.Helper()
	snap := &snapshot{revision: 7, policies: append(builtinPolicies(), extra...)}
	e := &evaluation{authority: a, snap: snap}
	dec, err := e.Decide(req)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	return dec
}

func mustCompile(t *testing.T, id, res, act, eff string, preds ...predicate) compiledPolicy {
	t.Helper()
	// These evaluation tests focus on effect/predicate logic, so they use the
	// explicit Any subject selector; subject-matching is covered in subject_test.go.
	cp, err := compileCustom(id, res, act, eff, AnySubject(), preds)
	if err != nil {
		t.Fatalf("compile %s: %v", id, err)
	}
	return cp
}

func TestBuiltinAgentDecisions(t *testing.T) {
	user := userAuthority(t, "u1", false)
	admin := userAuthority(t, "admin1", true)

	// user + system agent read -> allow
	if !evalWith(t, user, mustAgentRead(t, "a1", "", "system", false)).Allowed() {
		t.Error("user should read a system agent")
	}
	// user + assigned agent read -> allow
	if !evalWith(t, user, mustAgentRead(t, "a2", "", "user", true)).Allowed() {
		t.Error("user should read an assigned agent")
	}
	// user + unassigned user-scoped agent read -> default deny
	if evalWith(t, user, mustAgentRead(t, "a3", "", "user", false)).Allowed() {
		t.Error("user should NOT read an unassigned user-scoped agent")
	}
	// admin + unassigned user-scoped agent read -> allow (superuser)
	if !evalWith(t, admin, mustAgentRead(t, "a3", "", "user", false)).Allowed() {
		t.Error("admin should read any agent")
	}
}

func TestDefaultDenyForUnpoliciedResource(t *testing.T) {
	user := userAuthority(t, "u1", false)
	res, err := authz.NewResource(authz.ResourceSession, "s1", "u1")
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	req, err := authz.NewRequest(authz.ActionRead, res, authz.InvocationFacts{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	dec := evalWith(t, user, req)
	if dec.Allowed() {
		t.Fatal("session read must default-deny (no session built-in yet)")
	}
	if dec.Visibility() != authz.VisibilityForbidden {
		t.Fatalf("default-deny visibility = %v, want forbidden", dec.Visibility())
	}
}

func TestDenyOverridesBuiltinAllow(t *testing.T) {
	user := userAuthority(t, "u1", false)
	deny := mustCompile(t, "custom:deny-system-read", "agent", "read", "deny", Eq("scope", "system"))
	dec := evalWith(t, user, mustAgentRead(t, "a1", "", "system", false), deny)
	if dec.Allowed() {
		t.Fatal("custom deny must override the built-in system-agent allow")
	}
	if dec.PolicyID() != "custom:deny-system-read" {
		t.Fatalf("deciding policy = %q, want the custom deny", dec.PolicyID())
	}
}

func TestCustomAllowExtendsAccess(t *testing.T) {
	user := userAuthority(t, "u1", false)
	// Unassigned user-scoped agent normally default-denies; a custom allow grants it.
	allow := mustCompile(t, "custom:allow-user-scope", "agent", "read", "allow", Eq("scope", "user"))
	dec := evalWith(t, user, mustAgentRead(t, "a9", "", "user", false), allow)
	if !dec.Allowed() {
		t.Fatal("custom allow should grant read on a user-scoped agent")
	}
}

func TestInvalidRequestFailsClosed(t *testing.T) {
	user := userAuthority(t, "u1", false)
	snap := &snapshot{revision: 1, policies: builtinPolicies()}
	e := &evaluation{authority: user, snap: snap}
	// Zero request: invalid action + resource.
	dec, err := e.Decide(authz.Request{})
	if !errors.Is(err, authz.ErrInvalidRequest) {
		t.Fatalf("invalid request error = %v, want ErrInvalidRequest", err)
	}
	if dec.Allowed() {
		t.Fatal("invalid request must deny")
	}
}

func TestCompileRejectsUnknownCatalogValues(t *testing.T) {
	any := AnySubject()
	if _, err := compileCustom("x", "not_a_resource", "read", "allow", any, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown resource: got %v, want ErrInvalidPolicy", err)
	}
	if _, err := compileCustom("x", "agent", "fly", "allow", any, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown action: got %v, want ErrInvalidPolicy", err)
	}
	if _, err := compileCustom("x", "agent", "read", "maybe", any, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown effect: got %v, want ErrInvalidPolicy", err)
	}
	if _, err := compileCustom("x", "agent", "read", "allow", any, []predicate{{Attr: "ghost", Op: opEq, Value: "x"}}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("bad predicate: got %v, want ErrInvalidPolicy", err)
	}
	// A zero subject selector cannot compile.
	if _, err := compileCustom("x", "agent", "read", "allow", Selector{}, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("zero selector: got %v, want ErrInvalidPolicy", err)
	}
}
