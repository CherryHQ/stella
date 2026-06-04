package server

import (
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestScopedTokenAllowsOnlyMatchingAgentPath(t *testing.T) {
	claims := &auth.ScopedTokenClaims{AgentID: "agent-1", Scopes: auth.DefaultSandboxScopes}
	if !scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/agents/agent-1/skills", nil)) {
		t.Fatal("matching agent path should be allowed")
	}
	if scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/agents/agent-2/skills", nil)) {
		t.Fatal("different agent path should be denied")
	}
	if !scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/status", nil)) {
		t.Fatal("status should be allowed")
	}
	if !scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/vault/EMAIL_CONFIG", nil)) {
		t.Fatal("vault read should be allowed by the default sandbox scopes")
	}
}

func TestScopedTokenRequiresMappedScope(t *testing.T) {
	claims := &auth.ScopedTokenClaims{AgentID: "agent-1", Scopes: []string{"tasks:read"}}
	if !scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/tasks", nil)) {
		t.Fatal("tasks read should be allowed")
	}
	if scopedTokenAllowsRequest(claims, httptest.NewRequest("POST", "/api/tasks", nil)) {
		t.Fatal("tasks write should be denied without tasks:write")
	}
	if scopedTokenAllowsRequest(claims, httptest.NewRequest("GET", "/api/vault/EMAIL_CONFIG", nil)) {
		t.Fatal("vault read should be denied without vault:read")
	}
}
