package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

func TestPluginBackendPolicyRejectsCoreRuntimeBinaryNames(t *testing.T) {
	policy := pluginBackendPolicy(false)
	for _, resource := range systemplugins.EmbeddedRuntimeResources() {
		t.Run("reserved/"+resource.Name, func(t *testing.T) {
			definition, payload := testCLIBackendDefinition(t, resource.Name)
			enabled := false
			config := plugin.Config{
				ID: "config", PluginID: definition.ID,
				Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: payload, Revision: 1,
			}
			if err := policy.Validate(t.Context(), definition, config, nil); !errors.Is(err, plugin.ErrInvalidConfig) {
				t.Fatalf("reserved binary %q error = %v, want ErrInvalidConfig", resource.Name, err)
			}
		})
	}
	for _, resource := range systemplugins.RuntimeResources() {
		if resource.Embedded {
			continue
		}
		definition, payload := testCLIBackendDefinition(t, resource.Name)
		enabled := false
		config := plugin.Config{ID: "config", PluginID: definition.ID, Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: payload, Revision: 1}
		if err := policy.Validate(t.Context(), definition, config, nil); err != nil {
			t.Fatalf("mise-managed runtime binary %q error = %v, want nil", resource.Name, err)
		}
	}

	definition, payload := testCLIBackendDefinition(t, "ordinary-tool")
	enabled := false
	config := plugin.Config{
		ID: "config", PluginID: definition.ID,
		Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: payload, Revision: 1,
	}
	if err := policy.Validate(t.Context(), definition, config, nil); err != nil {
		t.Fatalf("ordinary binary error = %v, want nil", err)
	}

	definition, _ = testCLIBackendDefinition(t, "ordinary-tool")
	reservedPayload, err := json.Marshal(map[string]any{
		"binaries": map[string]any{
			systemplugins.EmbeddedRuntimeResources()[0].Name: map[string]string{"version": "1.0.0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	config = plugin.Config{
		ID: "config", PluginID: definition.ID,
		Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: reservedPayload, Revision: 1,
	}
	if err := policy.Validate(t.Context(), definition, config, nil); !errors.Is(err, plugin.ErrInvalidConfig) {
		t.Fatalf("reserved config binary error = %v, want ErrInvalidConfig", err)
	}
}

func testCLIBackendDefinition(t *testing.T, binaryName string) (plugin.Definition, json.RawMessage) {
	t.Helper()
	spec, err := json.Marshal(map[string]any{
		"description": "test",
		"binaries":    []map[string]string{{"name": binaryName, "tool": "github:owner/tool", "version": "1.0.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err = plugin.PublishDefinitionSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.Definition{
		ID: "test", DisplayName: "Test",
		Source: plugin.SourceBuiltin, Spec: spec, Revision: 1,
	}, spec
}

func TestPluginPolicyValidatesEveryComposableResource(t *testing.T) {
	const parentID = "10000000-0000-0000-0000-000000000001"
	spec := json.RawMessage(`{"binaries":[{"name":"demo-cli","tool":"github:example/demo","version":"1"}],"skills":[{"name":"demo-guide"}],"mcp_servers":{"main":{"url":"https://example.com/mcp","transport":"streamable_http","auth_type":"none"},"search":{"url":"https://example.org/mcp","transport":"streamable_http","auth_type":"none"}}}`)
	spec, err := plugin.PublishDefinitionSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	payload := spec
	def := plugin.Definition{ID: "demo", DisplayName: "Demo", Source: plugin.SourceBuiltin, Spec: spec, DefaultEnabled: true, Revision: 1}
	enabled := true
	cfg := plugin.Config{ID: parentID, PluginID: def.ID, Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: payload, CredentialRefs: json.RawMessage(`{}`), Revision: 1, MCPServers: []plugin.MCPServerChild{
		{ID: "10000000-0000-0000-0000-000000000002", ParentConfigID: parentID, ServerKey: "main"},
		{ID: "10000000-0000-0000-0000-000000000003", ParentConfigID: parentID, ServerKey: "search"},
	}}
	policy := pluginBackendPolicy(false)
	if err := policy.Validate(t.Context(), def, cfg, nil); err != nil {
		t.Fatalf("valid combined package: %v", err)
	}
	if err := policy.Validate(t.Context(), def, cfg, []string{"binaries"}); err != nil {
		t.Fatalf("CLI resource reset in a combined package: %v", err)
	}
	var unsafe map[string]any
	if err := json.Unmarshal(payload, &unsafe); err != nil {
		t.Fatal(err)
	}
	unsafe["mcp_servers"].(map[string]any)["search"].(map[string]any)["url"] = "http://127.0.0.1/admin"
	unsafePayload, err := json.Marshal(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Payload = unsafePayload
	if err := policy.Validate(t.Context(), def, cfg, nil); err == nil {
		t.Fatal("private endpoint accepted because CLI resources are valid")
	}
	cfg.Payload = payload
	unsafe["mcp_servers"].(map[string]any)["search"].(map[string]any)["url"] = "https://example.org/mcp"
	unsafe["binaries"] = append(unsafe["binaries"].([]any), map[string]any{"name": systemplugins.EmbeddedRuntimeResources()[0].Name, "tool": "github:owner/tool", "version": "1"})
	unsafePayload, err = json.Marshal(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Payload = unsafePayload
	if err := policy.Validate(t.Context(), def, cfg, nil); err == nil {
		t.Fatal("reserved executable accepted because MCP resources are valid")
	}
}

func TestPluginPolicyCLIPayloadResetDoesNotRequireMCP(t *testing.T) {
	def, payload := testCLIBackendDefinition(t, "ordinary-tool")
	enabled := true
	cfg := plugin.Config{ID: "config", PluginID: def.ID, Scope: plugin.ScopeSystem, Enabled: &enabled, Payload: payload, Revision: 1}
	if err := pluginBackendPolicy(false).Validate(t.Context(), def, cfg, []string{"binaries"}); err != nil {
		t.Fatalf("CLI reset: %v", err)
	}
}

func TestPluginPolicyCustomMCPResourcesLiveInConfig(t *testing.T) {
	const parentID = "10000000-0000-0000-0000-000000000001"
	enabled := true
	spec, err := plugin.PublishDefinitionSpec(json.RawMessage(`{"mcp_servers":{"main":{"url":"https://example.com/mcp","transport":"streamable_http","auth_type":"none"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	def := plugin.Definition{ID: "custom-demo", DisplayName: "Demo", Source: plugin.SourceCustom, Spec: spec, Revision: 1}
	cfg := plugin.Config{ID: parentID, PluginID: def.ID, Scope: plugin.ScopeSystem, Enabled: &enabled, Revision: 1, CredentialRefs: json.RawMessage(`{}`)}
	policy := pluginBackendPolicy(false)
	cfg.Payload = spec
	cfg.MCPServers = []plugin.MCPServerChild{{ID: "10000000-0000-0000-0000-000000000002", ParentConfigID: parentID, ServerKey: "main"}}
	if err := policy.Validate(t.Context(), def, cfg, nil); err != nil {
		t.Fatalf("custom MCP config: %v", err)
	}
	cfg.Payload = json.RawMessage(`{"mcp_servers":{}}`)
	cfg.MCPServers = nil
	if err := policy.Validate(t.Context(), def, cfg, nil); err != nil {
		t.Fatalf("last child removed: %v", err)
	}
}
