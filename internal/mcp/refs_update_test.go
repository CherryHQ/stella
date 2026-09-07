package mcp

import (
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestCommonMCPUpdateRefsPreservesSiblingAndParentRefs(t *testing.T) {
	current := plugin.Config{
		Scope: plugin.ScopeUser, UserID: "user-1",
		CredentialRefs: json.RawMessage(`{"session_env":{"name":"SESSION_ENV"},"mcp_servers":{"main":{"bearer":{"name":"old"}},"sibling":{"oauth_bundle":{"name":"sibling"}}}}`),
	}
	got, err := commonMCPUpdateRefs(current, "child-main", "main", AuthTypeBearer, CredentialModeShared, nil)
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]json.RawMessage
	if err := json.Unmarshal(got, &refs); err != nil {
		t.Fatal(err)
	}
	if _, ok := refs["session_env"]; !ok {
		t.Fatalf("parent refs lost: %s", got)
	}
	var children map[string]json.RawMessage
	if err := json.Unmarshal(refs["mcp_servers"], &children); err != nil {
		t.Fatal(err)
	}
	if _, ok := children["sibling"]; !ok {
		t.Fatalf("sibling refs lost: %s", got)
	}
	var main map[string]json.RawMessage
	if err := json.Unmarshal(children["main"], &main); err != nil {
		t.Fatal(err)
	}
	var bearer map[string]string
	if err := json.Unmarshal(main["bearer"], &bearer); err != nil {
		t.Fatal(err)
	}
	if bearer["name"] != credentialName("child-main") {
		t.Fatalf("target ref = %q, want %q", bearer["name"], credentialName("child-main"))
	}
}

func TestCommonMCPUpdateRefsPreservesOAuthSecretWhenOmitted(t *testing.T) {
	current := plugin.Config{
		ID: "parent-1", Scope: plugin.ScopeUser, UserID: "user-1",
		CredentialRefs: json.RawMessage(`{"mcp_servers":{"main":{"oauth_bundle":{"name":"bundle"},"oauth_client_secret":{"name":"existing-secret","scope":"user","user_id":"user-1"}}}}`),
	}
	got, err := commonMCPUpdateRefs(current, "child-main", "main", AuthTypeOAuth, CredentialModeShared, nil)
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]json.RawMessage
	if err := json.Unmarshal(got, &refs); err != nil {
		t.Fatal(err)
	}
	var children map[string]json.RawMessage
	if err := json.Unmarshal(refs["mcp_servers"], &children); err != nil {
		t.Fatal(err)
	}
	var main map[string]json.RawMessage
	if err := json.Unmarshal(children["main"], &main); err != nil {
		t.Fatal(err)
	}
	var secret map[string]string
	if err := json.Unmarshal(main["oauth_client_secret"], &secret); err != nil {
		t.Fatal(err)
	}
	if secret["name"] != "existing-secret" {
		t.Fatalf("preserved OAuth secret = %q, want existing-secret", secret["name"])
	}
}

func TestCommonMCPUpdateRefsExplicitEmptyClearsOAuthSecret(t *testing.T) {
	current := plugin.Config{
		ID: "parent-1", Scope: plugin.ScopeUser, UserID: "user-1",
		CredentialRefs: json.RawMessage(`{"mcp_servers":{"main":{"oauth_client_secret":{"name":"existing-secret"}}}}`),
	}
	empty := ""
	got, err := commonMCPUpdateRefs(current, "child-main", "main", AuthTypeOAuth, CredentialModeShared, &empty)
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]json.RawMessage
	if err := json.Unmarshal(got, &refs); err != nil {
		t.Fatal(err)
	}
	var children map[string]json.RawMessage
	if err := json.Unmarshal(refs["mcp_servers"], &children); err != nil {
		t.Fatal(err)
	}
	var main map[string]json.RawMessage
	if err := json.Unmarshal(children["main"], &main); err != nil {
		t.Fatal(err)
	}
	if _, ok := main["oauth_client_secret"]; ok {
		t.Fatalf("explicit empty secret was retained: %s", got)
	}
}
