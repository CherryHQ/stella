package mcp

import (
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestFormalMCPPayloadReadersRejectFlatShape(t *testing.T) {
	formal := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://example.test","transport":"sse","auth_type":"none"}}}`)
	got, err := decodeMCPPluginPayloadSingle(formal)
	if err != nil {
		t.Fatalf("formal single payload: %v", err)
	}
	if got.URL != "https://example.test" || got.Transport != "sse" {
		t.Fatalf("formal single payload = %#v", got)
	}
	if _, err := decodeMCPPluginPayloads(json.RawMessage(`{"url":"https://example.test","transport":"sse","auth_type":"none"}`)); err == nil {
		t.Fatal("flat payload accepted by payloads reader")
	}
	if _, err := decodeMCPPluginPayloadForKey(json.RawMessage(`{"url":"https://example.test","transport":"sse","auth_type":"none"}`), "main"); err == nil {
		t.Fatal("flat payload accepted by child reader")
	}
}

func TestFormalMCPCredentialRefsRequireNamedServer(t *testing.T) {
	cfg := plugin.Config{ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: plugin.ScopeUser, UserID: "user-1"}
	refs := json.RawMessage(`{"mcp_servers":{"main":{}}}`)
	if _, _, _, err := decodeMCPPluginCredentialRefsForKey(json.RawMessage(`{}`), cfg, "main", AuthTypeNone, CredentialModeShared); err != nil {
		t.Fatalf("unauthenticated child needs no credential refs: %v", err)
	}
	if _, _, _, err := decodeMCPPluginCredentialRefsForKey(refs, cfg, "main", AuthTypeNone, CredentialModeShared); err != nil {
		t.Fatalf("formal no-auth refs: %v", err)
	}
	for _, raw := range []string{
		`{"oauth_bundle":{"name":"bundle","mode":"shared"}}`,
	} {
		if _, _, _, err := decodeMCPPluginCredentialRefsForKey(json.RawMessage(raw), cfg, "main", AuthTypeNone, CredentialModeShared); err == nil {
			t.Fatalf("flat credential refs accepted: %s", raw)
		}
	}
	if _, _, _, err := decodeMCPPluginCredentialRefsForKey(refs, cfg, "other", AuthTypeBearer, CredentialModeShared); err == nil {
		t.Fatal("credential refs accepted a missing server key")
	}
}

func TestFormalMCPMergeAndReadUsesExactChild(t *testing.T) {
	definition := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://old.example","transport":"sse","auth_type":"none"},"search":{"url":"https://search.example","transport":"sse","auth_type":"none"}}}`)
	config := json.RawMessage(`{"mcp_servers":{"search":{"url":"https://new.example"}}}`)
	merged, err := plugin.MergeDefinitionConfig(definition, config)
	if err != nil {
		t.Fatalf("merge formal MCP parameters: %v", err)
	}
	payload, err := decodeMCPPluginPayloadForKey(merged, "search")
	if err != nil {
		t.Fatalf("read merged child: %v", err)
	}
	if payload.URL != "https://new.example" || payload.Transport != "sse" || payload.AuthType != AuthTypeNone {
		t.Fatalf("merged child = %#v", payload)
	}
	if _, err := decodeMCPPluginPayloadForKey(merged, "missing"); err == nil {
		t.Fatal("read accepted a child absent from the named map")
	}
}
