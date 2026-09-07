package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testCLIDefinition(t *testing.T) Definition {
	t.Helper()
	spec, err := json.Marshal(ResourcePayload{
		Description: "release",
		Category:    "system",
		Prompt:      "use the cli",
		Binaries: []BinaryResource{{
			Name: "demo", Tool: "github:owner/demo", Version: "1.0.0",
			Options: map[string]any{"asset_pattern": "demo_*", "future_option": "published"},
		}},
		Skills:        []SkillResource{{Name: "demo"}},
		SessionEnvs:   []SessionEnvResource{{EnvVar: "DEMO_TOKEN", Source: "oauth.access_token", Required: true}},
		OAuthProvider: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	return Definition{
		ID: "demo", DisplayName: "Demo",
		Source: SourceBuiltin,
		Spec:   spec, DefaultEnabled: true, Revision: 1,
	}
}

func TestBuiltinPackagePayloadsFollowScopes(t *testing.T) {
	definitions, err := BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		t.Run(definition.ID, func(t *testing.T) {
			for _, scope := range []Scope{ScopeSystem, ScopeUser} {
				config := Config{ID: "config", PluginID: definition.ID, Scope: scope, Revision: 1, Payload: definition.Spec}
				if scope == ScopeUser {
					config.UserID = "user-1"
				}
				for _, enabled := range []bool{true, false} {
					config.Enabled = &enabled
					if err := ValidatePayload(t.Context(), definition, config, nil); err != nil {
						t.Fatalf("scope=%s enabled=%v: %v", scope, enabled, err)
					}
				}
			}
		})
	}
}

func TestValidatePayloadAllowsMetadataOnlyBuiltinWithoutRuntimeIdentity(t *testing.T) {
	definition := Definition{
		ID: "metadata-only", DisplayName: "Metadata only", Source: SourceBuiltin,
		Spec: json.RawMessage(`{"description":"release metadata"}`), DefaultEnabled: true, Revision: 1,
	}
	config := Config{
		ID: "config", PluginID: definition.ID, Scope: ScopeSystem,
		Enabled: boolPtr(true), Payload: definition.Spec, Revision: 1,
	}
	if err := ValidatePayload(t.Context(), definition, config, nil); err != nil {
		t.Fatalf("metadata-only package: %v", err)
	}
	config.Payload = json.RawMessage(`{"description":"release metadata","binaries":[{"name":"unexpected","tool":"uv","version":"1"}]}`)
	if err := ValidatePayload(t.Context(), definition, config, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("metadata-only binary injection = %v, want invalid config", err)
	}
}

func TestValidatePayloadAllowsCustomEmptyDefinitionWithCLIConfig(t *testing.T) {
	definition := Definition{
		ID: "custom-cli", DisplayName: "Custom CLI", Source: SourceCustom,
		Spec: json.RawMessage(`{}`), Revision: 1,
	}
	config := Config{
		ID: "config", PluginID: definition.ID, Scope: ScopeSystem,
		Enabled: boolPtr(true), Payload: json.RawMessage(`{"binaries":[{"name":"custom","tool":"uv","version":"1"}]}`), Revision: 1,
	}
	if err := ValidatePayload(t.Context(), definition, config, nil); err != nil {
		t.Fatalf("custom CLI config: %v", err)
	}
}

func testUserPayload(t *testing.T, version string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(ResourcePayload{
		Description: "release",
		Category:    "system",
		Prompt:      "use the cli",
		Binaries: []BinaryResource{{
			Name: "demo", Tool: "github:owner/demo", Version: version,
			Options: map[string]any{"asset_pattern": "demo_*", "future_option": "published"},
		}},
		Skills:        []SkillResource{{Name: "demo"}},
		SessionEnvs:   []SessionEnvResource{{EnvVar: "DEMO_TOKEN", Source: "oauth.refresh_token", Required: true}},
		OAuthProvider: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestValidatePayloadAcceptsSystemAndUserOwnership(t *testing.T) {
	definition := testCLIDefinition(t)
	system := Config{ID: "system", PluginID: definition.ID, Scope: ScopeSystem, Revision: 1, Enabled: boolPtr(true), Payload: definition.Spec}
	if err := ValidatePayload(t.Context(), definition, system, nil); err != nil {
		t.Fatalf("system payload: %v", err)
	}

	user := Config{ID: "user", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(true), Payload: testUserPayload(t, "2.0.0"), CredentialRefs: json.RawMessage(`{"session_env":{"name":"DEMO_OAUTH","scope":"user","user_id":"user-1"}}`)}
	if err := ValidatePayload(t.Context(), definition, user, nil); err != nil {
		t.Fatalf("user payload: %v", err)
	}
	var userPayload ResourcePayload
	if err := json.Unmarshal(testUserPayload(t, "2.0.0"), &userPayload); err != nil {
		t.Fatal(err)
	}
	userPayload.Binaries[0].Options["extras"] = "spelling"
	encoded, err := json.Marshal(userPayload)
	if err != nil {
		t.Fatal(err)
	}
	user.Payload = encoded
	if err := ValidatePayload(t.Context(), definition, user, nil); err != nil {
		t.Fatalf("user safe option: %v", err)
	}
}

func TestValidatePayloadRejectsUserResourceIdentityChanges(t *testing.T) {
	definition := testCLIDefinition(t)
	cases := []struct {
		name   string
		mutate func(*ResourcePayload)
		resets []string
	}{
		{"binary name", func(p *ResourcePayload) { p.Binaries[0].Name = "other" }, nil},
		{"binary tool", func(p *ResourcePayload) { p.Binaries[0].Tool = "github:other/demo" }, nil},
		{"binary options", func(p *ResourcePayload) { p.Binaries[0].Options = map[string]any{"bin_path": "/tmp"} }, nil},
		{"published option changed", func(p *ResourcePayload) { p.Binaries[0].Options["future_option"] = "changed" }, nil},
		{"published option removed", func(p *ResourcePayload) { delete(p.Binaries[0].Options, "future_option") }, nil},
		{"extras type", func(p *ResourcePayload) { p.Binaries[0].Options["extras"] = true }, nil},
		{"unknown binary option", func(p *ResourcePayload) { p.Binaries[0].Options["new_hook"] = true }, nil},
		{"skill", func(p *ResourcePayload) { p.Skills[0].Name = "other" }, nil},
		{"prompt", func(p *ResourcePayload) { p.Prompt = "run anything" }, nil},
		{"session env identity", func(p *ResourcePayload) { p.SessionEnvs[0].EnvVar = "OTHER" }, nil},
		{"session env required", func(p *ResourcePayload) { p.SessionEnvs[0].Required = false }, nil},
		{"provider", func(p *ResourcePayload) { p.OAuthProvider = "other" }, nil},
		{"oauth binding injection", func(p *ResourcePayload) {
			p.OAuth = []OAuthRequirement{{Provider: "demo", Bindings: []OAuthBinding{{Credential: "access_token", EnvVar: "OTHER_TOKEN"}}}}
		}, nil},
		{"reset prompt", func(*ResourcePayload) {}, []string{"prompt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload ResourcePayload
			if err := json.Unmarshal(testUserPayload(t, "2.0.0"), &payload); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&payload)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			config := Config{ID: "user", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(true), Payload: raw}
			if err := ValidatePayload(t.Context(), definition, config, tc.resets); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want invalid config", err)
			}
		})
	}
}

func TestValidatePayloadRejectsSecretsUnknownFieldsAndBadRefs(t *testing.T) {
	definition := testCLIDefinition(t)
	cases := []struct {
		name      string
		payload   string
		refs      string
		wantError string
	}{
		{"unknown payload field", `{"unexpected":true}`, `{}`, "unknown field"},
		{"secret session env value", string(testUserPayload(t, "2.0.0")), `{"session_env":{"name":"x","scope":"user","user_id":"user-1"}}`, "value"},
		{"wrong ref owner", string(testUserPayload(t, "2.0.0")), `{"session_env":{"name":"x","scope":"user","user_id":"other"}}`, "owner"},
		{"plaintext ref", string(testUserPayload(t, "2.0.0")), `{"session_env":{"name":"x","scope":"user","user_id":"user-1","token":"secret"}}`, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.payload
			if tc.name == "secret session env value" {
				var decoded ResourcePayload
				if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
					t.Fatal(err)
				}
				decoded.SessionEnvs[0].Value = "secret"
				encoded, err := json.Marshal(decoded)
				if err != nil {
					t.Fatal(err)
				}
				payload = string(encoded)
			}
			config := Config{ID: "user", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(false), Payload: json.RawMessage(payload), CredentialRefs: json.RawMessage(tc.refs)}
			err := ValidatePayload(t.Context(), definition, config, nil)
			if !errors.Is(err, ErrInvalidConfig) || !contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want invalid config containing %q", err, tc.wantError)
			}
		})
	}
}

func TestValidatePayloadChecksDisabledPayloadAndResetFields(t *testing.T) {
	definition := testCLIDefinition(t)
	disabled := Config{ID: "user", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(false), Payload: json.RawMessage(`{"unexpected":true}`)}
	if err := ValidatePayload(t.Context(), definition, disabled, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("disabled malformed payload = %v, want invalid config", err)
	}
	if err := ValidatePayload(t.Context(), definition, Config{ID: "user", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(false), Payload: testUserPayload(t, "2.0.0")}, []string{"description"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("disabled unauthorized reset = %v, want invalid config", err)
	}
	var malformed ResourcePayload
	if err := json.Unmarshal(definition.Spec, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed.Binaries[0].Name = ""
	encoded, err := json.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	definition.Spec = encoded
	if err := ValidatePayload(t.Context(), definition, Config{ID: "negative", PluginID: definition.ID, Scope: ScopeUser, UserID: "user-1", Revision: 1, Enabled: boolPtr(false)}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil payload with malformed definition = %v, want invalid config", err)
	}
}

func contains(value, want string) bool {
	return strings.Contains(value, want)
}
