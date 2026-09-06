package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeLegacyMCPKeepsIdentityAndSecretBoundaries(t *testing.T) {
	catalog := NewCatalog()
	plan, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: string(ScopeUser), UserID: "user-1",
		Name: "GitHub Cloud", URL: "https://mcp.example.test", Transport: "sse", AuthType: "oauth",
		CredentialRef: "MCP_TOKEN_KEPT", CredentialMode: "shared",
		Metadata: map[string]any{"oauth": map[string]any{"client_secret_ref": "MCP_OAUTH_CLIENT_KEPT"}},
		Tools:    json.RawMessage(`[{"name":"create-issue"}]`), Enabled: true,
	}}}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Definitions) != 1 || len(plan.Configs) != 1 {
		t.Fatalf("plan sizes = %d definitions / %d configs", len(plan.Definitions), len(plan.Configs))
	}
	definition, config := plan.Definitions[0], plan.Configs[0]
	if definition.ID != "custom/0198f9a4-1b2c-7def-8123-456789abcdef" || definition.Namespace != "GitHub_Cloud" {
		t.Fatalf("MCP identity = %q/%q", definition.ID, definition.Namespace)
	}
	if string(definition.Spec) != `{}` || definition.ImplementationKey != "mcp" || definition.CreatorUserID != "user-1" {
		t.Fatalf("MCP definition safety fields = spec=%s key=%q creator=%q", definition.Spec, definition.ImplementationKey, definition.CreatorUserID)
	}
	if strings.Contains(string(definition.Spec), "example.test") || strings.Contains(string(definition.Spec), "secret") {
		t.Fatalf("definition contains endpoint or secret material: %s", definition.Spec)
	}
	if !strings.Contains(string(config.Payload), "https://mcp.example.test") || strings.Contains(string(config.Payload), "create-issue") || strings.Contains(string(config.Payload), "tool_map") {
		t.Fatalf("config mixed MCP observation into backend payload: %s", config.Payload)
	}
	if strings.Contains(string(config.Payload), "MCP_OAUTH_CLIENT_KEPT") {
		t.Fatal("OAuth locator duplicated into payload")
	}
	if strings.Contains(string(config.Payload), `"name"`) {
		t.Fatalf("config duplicated definition name: %s", config.Payload)
	}
	if !strings.Contains(string(config.CredentialRefs), "MCP_TOKEN_KEPT") || !strings.Contains(string(config.CredentialRefs), "MCP_OAUTH_CLIENT_KEPT") {
		t.Fatalf("credential boundary broken: payload=%s refs=%s", config.Payload, config.CredentialRefs)
	}
}

func TestNormalizeLegacyRejectsNamespaceAndPayloadCollisions(t *testing.T) {
	_, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{
		{ID: "0198f9a4-1b2c-7def-8123-456789abcdea", Scope: string(ScopeSystem), Name: "foo-bar", URL: "https://one.test", Transport: "sse", AuthType: "none", Enabled: true},
		{ID: "0198f9a4-1b2c-7def-8123-456789abcdeb", Scope: string(ScopeSystem), Name: "foo_bar", URL: "https://two.test", Transport: "sse", AuthType: "none", Enabled: true},
	}}, NewCatalog())
	if !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("namespace collision error = %v", err)
	}

	def := Definition{ID: "tool/test", Namespace: "test", DisplayName: "Test", Backend: BackendCLI, Source: SourceBuiltin, ImplementationKey: "tool/test", Spec: json.RawMessage(`{"name":"test"}`), DefaultEnabled: true, Revision: 1}
	catalog := NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeLegacySnapshot(LegacySnapshot{
		Plugins:           []LegacyPlugin{{ID: def.ID, Enabled: true, Config: json.RawMessage(`{"same":1}`)}},
		ManifestOverrides: []LegacyManifestOverride{{PluginID: def.ID, Config: `{"$sparse":true,"same":2}`}},
	}, catalog)
	if !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("payload collision error = %v", err)
	}
}

func TestNormalizeLegacyRejectsUnsupportedMCPMetadata(t *testing.T) {
	_, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: string(ScopeSystem), Name: "github", URL: "https://mcp.example.test", Transport: "sse", AuthType: "none", Enabled: true,
		Metadata: map[string]any{"token": "secret"},
	}}}, NewCatalog())
	if !errors.Is(err, ErrLegacyMigrationConflict) || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("unsupported metadata error = %v", err)
	}
}

func TestNormalizeLegacyRejectsManifestIdentityChange(t *testing.T) {
	def := Definition{ID: "tool/test", Namespace: "test", DisplayName: "Test", Backend: BackendCLI, Source: SourceBuiltin, ImplementationKey: "tool/test", Spec: json.RawMessage(`{}`), DefaultEnabled: true, Revision: 1}
	catalog := NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	_, err := NormalizeLegacySnapshot(LegacySnapshot{ManifestOverrides: []LegacyManifestOverride{
		{PluginID: def.ID, Config: `{"$sparse":true,"display_name":"Other"}`},
	}}, catalog)
	if !errors.Is(err, ErrLegacyMigrationConflict) || !strings.Contains(err.Error(), "display_name") {
		t.Fatalf("identity override error = %v", err)
	}
}

func TestNormalizeLegacyRejectsLiteralSessionEnv(t *testing.T) {
	def := Definition{ID: "tool/test", Namespace: "test", DisplayName: "Test", Backend: BackendCLI, Source: SourceBuiltin, ImplementationKey: "tool/test", Spec: json.RawMessage(`{}`), DefaultEnabled: true, Revision: 1}
	catalog := NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	_, err := NormalizeLegacySnapshot(LegacySnapshot{ManifestOverrides: []LegacyManifestOverride{
		{PluginID: def.ID, Config: `{"$sparse":true,"session_env":[{"env_var":"TOKEN","source":"literal","value":"secret"}]}`},
	}}, catalog)
	if !errors.Is(err, ErrLegacyMigrationConflict) || !strings.Contains(err.Error(), "session_env") {
		t.Fatalf("literal session env error = %v", err)
	}
}

func TestNormalizeLegacyMapsCustomCLIToStableCustomIdentity(t *testing.T) {
	oldID := "tool/private-cli"
	plan, err := NormalizeLegacySnapshot(LegacySnapshot{
		ManifestOverrides: []LegacyManifestOverride{{PluginID: oldID, Enabled: importBoolPtr(true), Config: `{"name":"private-cli","display_name":"Private CLI","prompt":"use it"}`}},
		Plugins:           []LegacyPlugin{{ID: oldID, Enabled: true, Config: json.RawMessage(`{"version":"1"}`)}},
	}, NewCatalog())
	if err != nil {
		t.Fatal(err)
	}
	wantID := legacyCustomDefinitionID(oldID)
	if len(plan.Definitions) != 1 || plan.Definitions[0].ID != wantID || len(plan.Configs) != 1 || plan.Configs[0].PluginID != wantID || plan.Configs[0].ID != strings.TrimPrefix(wantID, "custom/") {
		t.Fatalf("custom identity mapping = %#v / %#v", plan.Definitions, plan.Configs)
	}
}

func TestNormalizeLegacyAllowsSameNamespaceForDifferentOwners(t *testing.T) {
	plan, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{
		{ID: "0198f9a4-1b2c-7def-8123-456789abcdea", Scope: string(ScopeUser), UserID: "user-a", Name: "git-hub", URL: "https://one.test", Transport: "sse", AuthType: "none", Enabled: true},
		{ID: "0198f9a4-1b2c-7def-8123-456789abcdeb", Scope: string(ScopeUser), UserID: "user-b", Name: "git hub", URL: "https://two.test", Transport: "sse", AuthType: "none", Enabled: true},
	}}, NewCatalog())
	if err != nil || len(plan.Configs) != 2 {
		t.Fatalf("different-owner namespace result = %#v, err=%v", plan.Configs, err)
	}
}

func TestConvertLegacyToolOverrideResolvesExactMCPRegistration(t *testing.T) {
	registration := LegacyMCPRegistration{ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: string(ScopeUser), UserID: "user-1", Name: "GitHub", Tools: json.RawMessage(`[{"name":"create-issue"}]`)}
	migration, err := ConvertLegacyToolOverride(LegacyToolOverride{ID: "override-1", ToolName: "mcp__GitHub__create_issue", Scope: string(ScopeUser), UserID: "user-1", Enabled: false}, []LegacyMCPRegistration{registration})
	if err != nil {
		t.Fatal(err)
	}
	if migration.NewName != "GitHub__create_issue" || migration.ConfigID != registration.ID || migration.Enabled {
		t.Fatalf("migration = %#v", migration)
	}
}

func importBoolPtr(value bool) *bool { return &value }

func TestPreviewIncludesShippedNamespaceClaimsWithoutInventingConfigIDs(t *testing.T) {
	catalog := NewCatalog()
	if err := catalog.Register(testDefinition()); err != nil {
		t.Fatal(err)
	}
	registration := LegacyMCPRegistration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: string(ScopeSystem),
		Name: "test", URL: "https://mcp.example.test", Transport: "sse", AuthType: "none", Enabled: true,
	}
	if _, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{registration}}, catalog); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("unconfigured builtin namespace collision = %v", err)
	}
	plan, err := NormalizeLegacySnapshot(LegacySnapshot{}, catalog)
	if err != nil || len(plan.Configs) != 0 {
		t.Fatalf("default claim fabricated config identity: %#v, %v", plan.Configs, err)
	}
	registration.Scope, registration.UserID = string(ScopeUser), "user"
	if _, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{registration}}, catalog); err != nil {
		t.Fatalf("different scope namespace claim denied: %v", err)
	}
	registration.Name = "git--hub"
	if _, err := NormalizeLegacySnapshot(LegacySnapshot{MCP: []LegacyMCPRegistration{registration}}, catalog); err == nil {
		t.Fatal("invalid normalized namespace accepted without a discovered tool catalog")
	}
}

func TestToolOverrideMigrationHandlesSharedRegistrationAndGoPlugins(t *testing.T) {
	registration := LegacyMCPRegistration{ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Scope: "system", Name: "shared", Tools: json.RawMessage(`[{"name":"read"}]`)}
	override := LegacyToolOverride{ID: "policy", ToolName: "mcp__shared__read", Scope: "user", UserID: "user"}
	mapped, err := ConvertLegacyToolOverride(override, []LegacyMCPRegistration{registration})
	if err != nil || mapped.PluginID != "custom/"+registration.ID || mapped.UserID != "user" || mapped.Enabled {
		t.Fatalf("shared deny mapping = %#v, %v", mapped, err)
	}
	private := registration
	private.ID, private.Scope, private.UserID = "0198f9a4-1b2c-7def-8123-456789abcdea", "user", "user"
	if _, err := ConvertLegacyToolOverride(override, []LegacyMCPRegistration{registration, private}); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("ambiguous policy mapping = %v", err)
	}
	for _, name := range []string{"email_message_send", "recally_entry_add", "scheduler_job_create"} {
		override.ToolName = name
		mapped, err := ConvertLegacyToolOverride(override, nil)
		if err != nil || mapped.PluginID == "" || mapped.Enabled || !strings.Contains(mapped.NewName, "__") {
			t.Fatalf("Go plugin policy %s = %#v, %v", name, mapped, err)
		}
	}
	override.ToolName = "email_impostor"
	mapped, err = ConvertLegacyToolOverride(override, nil)
	if err != nil || mapped.PluginID != "" {
		t.Fatal("a prefix invented a plugin-owned tool")
	}
}
