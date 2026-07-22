package credential

import (
	"strings"
	"testing"
)

func TestEnforceRequiresMappedScope(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"goals:read"}}

	if err := Enforce(p, "GET", "/api/goals"); err != nil {
		t.Fatalf("goals read should be allowed: %v", err)
	}
	if err := Enforce(p, "POST", "/api/goals"); err == nil {
		t.Fatal("goals write must be denied without goals:write")
	}
	if err := Enforce(p, "GET", "/api/users/me"); err == nil {
		t.Fatal("unmapped users/me profile must be denied")
	}
}

// A PAT with no matching scope must be DENIED, not silently full-access. This
// locks CRITICAL #1 (enforcement gated on kind/scopes, not info.Scoped != nil).
func TestEnforcePATWithoutScopeIsDenied(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"goals:read"}}

	if err := Enforce(p, "GET", "/api/goals"); err != nil {
		t.Fatalf("PAT with goals:read should reach goals read: %v", err)
	}
	if err := Enforce(p, "GET", "/api/workflows"); err == nil {
		t.Fatal("PAT without workflows scope must be denied on /api/workflows")
	}
	empty := &Principal{Kind: KindPAT, UserID: "u1", Scopes: nil}
	if err := Enforce(empty, "GET", "/api/goals"); err == nil {
		t.Fatal("PAT with no scopes must be denied, not granted full access")
	}
}

func TestEnforceRejectsRetiredLegacyKind(t *testing.T) {
	p := &Principal{Kind: Kind("legacy_stella_token"), UserID: "u1"}
	for _, path := range []string{"/api/workflows", "/api/goals", "/api/anything-unmapped"} {
		if err := Enforce(p, "POST", path); err == nil {
			t.Fatalf("retired legacy token kind must not bypass API-scope checks for %s", path)
		}
	}
}

// Unregistered routes fail closed for bearer credentials.
func TestEnforceUnregisteredRouteDenied(t *testing.T) {
	p := &Principal{Kind: KindPAT, Scopes: []string{"goals:*", "channels:*"}}
	if err := Enforce(p, "GET", "/api/channels"); err == nil {
		t.Fatal("a resource not exposed to bearer credentials must be denied")
	}
	if err := Enforce(p, "GET", "/agents"); err == nil {
		t.Fatal("non-API page routes must be denied for bearer credentials")
	}
}

// A PAT reaches the top-level external APIs but must never reach the
// sandbox-internal vault/oauth surface, even if it somehow presented those
// scopes.
func TestEnforcePATReachesExternalButNotSandboxInternal(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"email:*", "skills:*", "vault:*", "oauth:*"}}

	if err := Enforce(p, "POST", "/api/email/send"); err != nil {
		t.Fatalf("PAT with email:* must reach /api/email/send: %v", err)
	}
	if err := Enforce(p, "GET", "/api/skills"); err != nil {
		t.Fatalf("PAT with skills:* must reach /api/skills: %v", err)
	}
	if err := Enforce(p, "GET", "/api/vault/EMAIL_CONFIG"); err == nil {
		t.Fatal("PAT must never reach vault even with vault:* present")
	}
	if err := Enforce(p, "POST", "/api/users/me/oauth/github/start"); err == nil {
		t.Fatal("PAT must never reach oauth even with oauth:* present")
	}
}

func TestSaveAsWorkflowRequiresWorkflowWrite(t *testing.T) {
	scope, registered := RequiredScope("POST", "/api/goals/goal-1/save-as-workflow")
	if !registered || scope != "workflows:write" {
		t.Fatalf("scope=%q registered=%v want workflows:write true", scope, registered)
	}
	goalsOnly := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"goals:write"}}
	if err := Enforce(goalsOnly, "POST", "/api/goals/goal-1/save-as-workflow"); err == nil {
		t.Fatal("goals:write alone must not allow save-as-workflow")
	}
	for _, scopes := range [][]string{{"workflows:write"}, {"workflows:*"}} {
		p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: scopes}
		if err := Enforce(p, "POST", "/api/goals/goal-1/save-as-workflow"); err != nil {
			t.Fatalf("%v should allow save-as-workflow: %v", scopes, err)
		}
	}
}

func TestRequiredScopeUnknownAgentSubResourceFailsClosed(t *testing.T) {
	if _, registered := RequiredScope("POST", "/api/agents/a1/secrets"); registered {
		t.Fatal("unknown agent sub-resource must be registered=false (fail-closed)")
	}
	// Known sub-resources stay registered.
	for _, path := range []string{
		"/api/agents/a1/sessions",
		"/api/agents/a1/skills",
		"/api/agents/a1/scheduler/jobs",
		"/api/agents/a1/projects",
		"/api/agents/a1/users",
		"/api/agents/a1",
	} {
		if _, registered := RequiredScope("GET", path); !registered {
			t.Errorf("known route %s must be registered", path)
		}
	}
}

func TestValidatePATScopes(t *testing.T) {
	if _, ok := ValidatePATScopes(nil); ok {
		t.Fatal("empty scope set must be rejected")
	}
	if _, ok := ValidatePATScopes([]string{"workflows:read", "goals:*"}); !ok {
		t.Fatal("valid exposable scopes should pass")
	}
	if bad, ok := ValidatePATScopes([]string{"vault:read"}); ok || bad != "vault:read" {
		t.Fatalf("vault must not be grantable to a PAT; got bad=%q ok=%v", bad, ok)
	}
	if bad, ok := ValidatePATScopes([]string{"oauth:*"}); ok || bad != "oauth:*" {
		t.Fatalf("oauth must not be grantable to a PAT; got bad=%q ok=%v", bad, ok)
	}
	if _, ok := ValidatePATScopes([]string{"goals"}); ok {
		t.Fatal("malformed scope (no action) must be rejected")
	}
	if _, ok := ValidatePATScopes([]string{"nope:read"}); ok {
		t.Fatal("unknown resource must be rejected")
	}
}

func TestExposableScopesExcludeSandboxInternal(t *testing.T) {
	for _, s := range ExposableScopes() {
		if strings.HasPrefix(s, "vault:") || strings.HasPrefix(s, "oauth:") {
			t.Fatalf("sandbox-internal scope %q must not be exposable to PATs", s)
		}
	}
}
