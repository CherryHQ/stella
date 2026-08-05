package credential

import "testing"

func TestEnforcePATInheritsOwnerAuthorityForAPIRoutes(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1"}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{"GET", "/api/goals"},
		{"POST", "/api/goals"},
		{"GET", "/api/auth/me"},
		{"GET", "/api/users"},
		{"GET", "/api/vault/EMAIL_CONFIG"},
		{"GET", "/api/nonexistent-xyz"},
	} {
		if err := Enforce(p, tc.method, tc.path); err != nil {
			t.Errorf("PAT %s %s: %v", tc.method, tc.path, err)
		}
	}

	if err := Enforce(p, "GET", "/agents"); err == nil {
		t.Fatal("PAT must not call non-API routes")
	}
}

func TestEnforceOAuthRetainsRouteScopeEnforcement(t *testing.T) {
	p := &Principal{Kind: KindOAuth, UserID: "u1", Scopes: []string{"agent:read"}}

	if err := Enforce(p, "GET", "/api/agents"); err != nil {
		t.Fatalf("OAuth agent:read must reach agent reads: %v", err)
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{"POST", "/api/agents"},
		{"GET", "/api/goals"},
		{"GET", "/api/users"},
		{"GET", "/api/vault/EMAIL_CONFIG"},
		{"GET", "/api/nonexistent-xyz"},
		{"GET", "/agents"},
	} {
		if err := Enforce(p, tc.method, tc.path); err == nil {
			t.Errorf("OAuth %s %s must be denied", tc.method, tc.path)
		}
	}
}

func TestEnforceRejectsUnknownKind(t *testing.T) {
	p := &Principal{Kind: Kind("legacy_stella_token"), UserID: "u1"}
	if err := Enforce(p, "GET", "/api/goals"); err == nil {
		t.Fatal("unknown credential kind must be denied")
	}
}

func TestSaveAsWorkflowRequiresOAuthWorkflowWrite(t *testing.T) {
	scope, registered := RequiredScope("POST", "/api/goals/goal-1/save-as-workflow")
	if !registered || scope != "workflows:write" {
		t.Fatalf("scope=%q registered=%v want workflows:write true", scope, registered)
	}
	goalsOnly := &Principal{Kind: KindOAuth, UserID: "u1", Scopes: []string{"goals:write"}}
	if err := Enforce(goalsOnly, "POST", "/api/goals/goal-1/save-as-workflow"); err == nil {
		t.Fatal("OAuth goals:write alone must not allow save-as-workflow")
	}
	for _, scopes := range [][]string{{"workflows:write"}, {"workflows:*"}} {
		p := &Principal{Kind: KindOAuth, UserID: "u1", Scopes: scopes}
		if err := Enforce(p, "POST", "/api/goals/goal-1/save-as-workflow"); err != nil {
			t.Fatalf("OAuth %v should allow save-as-workflow: %v", scopes, err)
		}
	}
}

func TestRequiredScopeUnknownAgentSubResourceFailsClosed(t *testing.T) {
	if _, registered := RequiredScope("POST", "/api/agents/a1/secrets"); registered {
		t.Fatal("unknown agent sub-resource must be registered=false (fail-closed)")
	}
	for _, path := range []string{
		"/api/agents/a1/sessions",
		"/api/agents/a1/skills",
		"/api/agents/a1/scheduler/jobs",
		"/api/agents/a1/projects",
		"/api/agents/a1/users",
		"/api/agents/a1/provider-credentials",
		"/api/agents/a1",
	} {
		if _, registered := RequiredScope("GET", path); !registered {
			t.Errorf("known route %s must be registered", path)
		}
	}
}
