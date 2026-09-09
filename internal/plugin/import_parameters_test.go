package plugin

import (
	"encoding/json"
	"testing"
)

func TestNormalizeImportedConfigPayloadUsesNamedParameters(t *testing.T) {
	spec, err := PublishDefinitionSpec([]byte(`{"binaries":[{"name":"uv","tool":"uv","version":"1"}],"mcp_servers":{"main":{"url":"https://old.example","transport":"sse","auth_type":"none"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"description":null,"version":"old","binaries":[{"name":"uv","version":"2","options":{"channel":"latest"}}],"mcp_servers":{"main":{"url":"https://new.example"}},"skills":null}`)
	normalized, err := normalizeImportedConfigPayload(spec, raw)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := DecodeConfigParameters(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if parameters.Binaries["uv"].Version != "2" || parameters.Binaries["uv"].Options["channel"] != "latest" {
		t.Fatalf("binary parameters = %#v", parameters.Binaries["uv"])
	}
	if parameters.MCPServers["main"].URL != "https://new.example" {
		t.Fatalf("MCP parameters = %#v", parameters.MCPServers["main"])
	}
	if string(normalized) == string(raw) {
		t.Fatalf("migration retained legacy payload: %s", normalized)
	}
}
