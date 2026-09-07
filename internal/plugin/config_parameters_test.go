package plugin

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeConfigParametersIsStrict(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown top level", raw: `{"prompt":"override"}`},
		{name: "immutable version", raw: `{"version":"2"}`},
		{name: "null immutable requirements", raw: `{"oauth":null}`},
		{name: "legacy array", raw: `{"binaries":[{"name":"uv","version":"2"}]}`},
		{name: "session source", raw: `{"session_env":{"GH_TOKEN":{"source":"oauth:other"}}}`},
		{name: "trailing object", raw: `{} {}`},
		{name: "null document", raw: `null`},
		{name: "null binary map", raw: `{"binaries":null}`},
		{name: "null binary", raw: `{"binaries":{"uv":null}}`},
		{name: "unknown binary field", raw: `{"binaries":{"uv":{"tool":"other"}}}`},
		{name: "null binary field", raw: `{"binaries":{"uv":{"version":null}}}`},
		{name: "unknown MCP field", raw: `{"mcp_servers":{"main":{"name":"other"}}}`},
		{name: "null MCP field", raw: `{"mcp_servers":{"main":{"url":null}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeConfigParameters([]byte(test.raw)); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("DecodeConfigParameters(%s) error = %v, want ErrInvalidConfig", test.raw, err)
			}
		})
	}
}

func TestApplyConfigParametersDoesNotMutateDeclaration(t *testing.T) {
	declaration := ResourcePayload{
		Binaries: []BinaryResource{{Name: "uv", Tool: "uv", Version: "1"}},
		MCPServers: map[string]MCPServerResource{
			"main": {URL: "https://old.example", Transport: "sse"},
		},
	}
	parameters := ConfigParameters{
		Binaries:   map[string]BinaryParameters{"uv": {Version: "2"}},
		MCPServers: map[string]MCPParameters{"main": {URL: "https://new.example"}},
	}
	effective, err := applyConfigParameters(declaration, parameters)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Binaries[0].Version != "2" || effective.MCPServers["main"].URL != "https://new.example" {
		t.Fatalf("parameters were not applied: %#v", effective)
	}
	if declaration.Binaries[0].Version != "1" || declaration.MCPServers["main"].URL != "https://old.example" {
		t.Fatalf("one config mutated the shared declaration: %#v", declaration)
	}
}

func TestMergeObjectsAppliesOnlyDeclaredResources(t *testing.T) {
	base := []byte(`{"binaries":[{"name":"uv","tool":"uv","version":"1","options":{"channel":"stable"}}],"mcp_servers":{"main":{"url":"https://old.example","transport":"sse","auth_type":"none"}}}`)
	overlay := []byte(`{"binaries":{"uv":{"version":"2","options":{"channel":"latest"}}},"mcp_servers":{"main":{"url":"https://new.example","headers":{"X-Token":"ref"}}}}`)
	merged, err := MergeDefinitionConfig(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	got := string(merged)
	for _, want := range []string{`"name":"uv"`, `"tool":"uv"`, `"version":"2"`, `"url":"https://new.example"`, `"transport":"sse"`, `"X-Token":"ref"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged payload %s misses %s", got, want)
		}
	}
	if _, err := MergeDefinitionConfig(base, []byte(`{"binaries":{"other":{"version":"2"}}}`)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown binary error = %v, want ErrInvalidConfig", err)
	}
	if _, err := MergeDefinitionConfig(base, []byte(`{"mcp_servers":{"other":{"url":"https://other.example"}}}`)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown MCP error = %v, want ErrInvalidConfig", err)
	}
}

func TestRemoteMCPConfigSelectsItsOwnChildren(t *testing.T) {
	declaration := ResourcePayload{
		Origin: "remote_mcp",
		MCPServers: map[string]MCPServerResource{
			"one": {URL: "https://one.example"},
			"two": {URL: "https://two.example"},
		},
	}
	effective, err := applyConfigParameters(declaration, ConfigParameters{
		MCPServers: map[string]MCPParameters{"one": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective.MCPServers) != 1 || effective.MCPServers["one"].URL != "https://one.example" {
		t.Fatalf("config inherited another config's children: %#v", effective.MCPServers)
	}
	empty, err := applyConfigParameters(declaration, ConfigParameters{})
	if err != nil || len(empty.MCPServers) != 0 {
		t.Fatalf("deleted final child returned: %#v, %v", empty.MCPServers, err)
	}
	declaration.Origin = "package"
	fixed, err := applyConfigParameters(declaration, ConfigParameters{})
	if err != nil || len(fixed.MCPServers) != 2 {
		t.Fatalf("package config lost fixed membership: %#v, %v", fixed.MCPServers, err)
	}
}
