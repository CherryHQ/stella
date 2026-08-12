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
		{"GET", "/api/users/user-1"},
		{"GET", "/api/users/user-1/default-agent"},
		{"GET", "/api/users/user-1/notify-identity"},
		{"GET", "/api/users/user-1/agents"},
		{"GET", "/api/users/me/memories"},
		{"GET", "/api/users/me/oauth-client-scopes"},
		{"GET", "/api/users/me/oauth/github/connected"},
		{"GET", "/api/vault/EMAIL_CONFIG"},
		{"GET", "/api/providers"},
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

func TestEnforcePATCredentialRouteFence(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", IsAdmin: true}

	for _, path := range []string{
		"/api/users/me/tokens",
		"/api/users/me/tokens/token-1",
		"/api/admin/provisioning-tokens",
		"/api/admin/provisioning-tokens/token-1",
		"/api/provisioned-users",
		"/api/provisioned-users/user-1/rotate-token",
		"/api/users/me/oauth-clients",
		"/api/users/me/oauth-clients/client-1/rotate-secret",
		"/api/users/me/authorized-apps",
		"/api/users/me/authorized-apps/client-1",
		"/api/auth/sessions",
		"/api/auth/sessions/session-1",
		"/api/users/me/identities",
		"/api/users/me/identities/identity-1",
		"/api/users/user-1/identities/login",
		"/api/users/me/password",
		"/api/users/me/link-code",
		"/api/users/user-1/role",
		"/api/users/user-1/active",
	} {
		if err := Enforce(p, "POST", path); err == nil {
			t.Errorf("admin PAT POST %s must be denied", path)
		}
	}

	// These look similar but are intentionally outside the fence. Exact rules
	// must not grow descendants, and family matching must stay segment-aware.
	for _, path := range []string{
		"/api/auth/session",
		"/api/users/me/oauth-client-scopes",
		"/api/users/me/oauth/github/connected",
		"/api/users/me/password/reset",
		"/api/users/user-1/roles",
		"/api/users/user-1/active/history",
	} {
		if err := Enforce(p, "GET", path); err != nil {
			t.Errorf("admin PAT GET %s must remain outside the fence: %v", path, err)
		}
	}
}

func TestEnforceProvisioningTokenStrictAllowlist(t *testing.T) {
	p := &Principal{Kind: KindProvisioning, UserID: "admin", CredentialID: "token-1"}
	for _, path := range []string{
		"/api/provisioned-users",
		"/api/provisioned-users/user-1",
		"/api/provisioned-users/user-1/rotate",
	} {
		if err := Enforce(p, "POST", path); err != nil {
			t.Errorf("provisioning token POST %s: %v", path, err)
		}
	}
	for _, path := range []string{
		"/api/provisioned-users-extra",
		"/api/admin/provisioning-tokens",
		"/api/agents",
		"/agents",
	} {
		if err := Enforce(p, "GET", path); err == nil {
			t.Errorf("provisioning token GET %s must be denied", path)
		}
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
		{"GET", "/api/provisioned-users"},
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

func TestLibraryFileRoutesRequireLibraryScopes(t *testing.T) {
	readScope, registered := RequiredScope("GET", "/api/library-files/file-id")
	if !registered || readScope != "library:read" {
		t.Fatalf("GET Library scope = %q registered=%v, want library:read true", readScope, registered)
	}
	writeScope, registered := RequiredScope("POST", "/api/library-files")
	if !registered || writeScope != "library:write" {
		t.Fatalf("POST Library scope = %q registered=%v, want library:write true", writeScope, registered)
	}

	readOnly := &Principal{Kind: KindOAuth, UserID: "u1", Scopes: []string{"library:read"}}
	if err := Enforce(readOnly, "GET", "/api/library-files"); err != nil {
		t.Fatalf("library:read should reach the list: %v", err)
	}
	if err := Enforce(readOnly, "DELETE", "/api/library-files/file-id"); err == nil {
		t.Fatal("library:read must not allow file deletion")
	}
}
