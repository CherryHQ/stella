package github

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg == nil {
		t.Fatal("defaultConfig() returned nil")
	}

	expected := map[string]string{
		"client_id":     "",
		"client_secret": "",
		"redirect_url":  "",
	}

	for key, want := range expected {
		if got, ok := cfg[key]; !ok {
			t.Errorf("defaultConfig() missing key %q", key)
		} else if got != want {
			t.Errorf("defaultConfig()[%q] = %v, want %v", key, got, want)
		}
	}
}

func TestConfigSchema(t *testing.T) {
	schema := configSchema()
	if schema == nil {
		t.Fatal("configSchema() returned nil")
	}

	// Check type
	if typ, ok := schema["type"].(string); !ok || typ != "object" {
		t.Errorf("schema[type] = %v, want 'object'", schema["type"])
	}

	// Check required fields
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema[required] not found or wrong type")
	}
	if len(required) != 2 {
		t.Errorf("len(required) = %d, want 2", len(required))
	}

	// Check properties exist
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema[properties] not found or wrong type")
	}

	expectedProps := []string{"client_id", "client_secret", "redirect_url"}
	for _, prop := range expectedProps {
		if _, ok := properties[prop]; !ok {
			t.Errorf("schema missing property %q", prop)
		}
	}
}

func TestDecodeConfig_Valid(t *testing.T) {
	raw := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "test-secret",
		"redirect_url":  "https://example.com/callback",
	}

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}

	if cfg.ClientID != "test-client-id" {
		t.Errorf("ClientID = %q, want %q", cfg.ClientID, "test-client-id")
	}
	if cfg.ClientSecret != "test-secret" {
		t.Errorf("ClientSecret = %q, want %q", cfg.ClientSecret, "test-secret")
	}
	if cfg.RedirectURL != "https://example.com/callback" {
		t.Errorf("RedirectURL = %q, want %q", cfg.RedirectURL, "https://example.com/callback")
	}
}

func TestDecodeConfig_Empty(t *testing.T) {
	raw := map[string]any{}

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}

	if cfg.ClientID != "" || cfg.ClientSecret != "" || cfg.RedirectURL != "" {
		t.Error("decodeConfig() with empty map should return empty config")
	}
}

func TestDecodeConfig_InvalidTypes(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "client_id not string",
			raw:  map[string]any{"client_id": 123},
			want: "client_id",
		},
		{
			name: "client_secret not string",
			raw:  map[string]any{"client_secret": true},
			want: "client_secret",
		},
		{
			name: "redirect_url not string",
			raw:  map[string]any{"redirect_url": []string{}},
			want: "redirect_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeConfig(tt.raw)
			if err == nil {
				t.Fatal("decodeConfig() expected error")
			}
			if err.Error() != tt.want+": must be a string" {
				t.Errorf("error = %q, want %q", err.Error(), tt.want+": must be a string")
			}
		})
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	raw := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "test-secret",
		"redirect_url":  "https://example.com/callback",
	}

	if err := validateConfig(raw); err != nil {
		t.Errorf("validateConfig() error = %v", err)
	}
}

func TestValidateConfig_MissingClientID(t *testing.T) {
	raw := map[string]any{
		"client_secret": "test-secret",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for missing client_id")
	}
	if err.Error() != "client_id: required" {
		t.Errorf("error = %q, want %q", err.Error(), "client_id: required")
	}
}

func TestValidateConfig_MissingClientSecret(t *testing.T) {
	raw := map[string]any{
		"client_id": "test-client-id",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for missing client_secret")
	}
	if err.Error() != "client_secret: required" {
		t.Errorf("error = %q, want %q", err.Error(), "client_secret: required")
	}
}

func TestValidateConfig_EmptyClientID(t *testing.T) {
	raw := map[string]any{
		"client_id":     "",
		"client_secret": "test-secret",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for empty client_id")
	}
	if err.Error() != "client_id: required" {
		t.Errorf("error = %q, want %q", err.Error(), "client_id: required")
	}
}

func TestValidateConfig_EmptyClientSecret(t *testing.T) {
	raw := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for empty client_secret")
	}
	if err.Error() != "client_secret: required" {
		t.Errorf("error = %q, want %q", err.Error(), "client_secret: required")
	}
}

func TestValidateConfig_InvalidDecode(t *testing.T) {
	raw := map[string]any{
		"client_id": 123,
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error")
	}
	if err.Error() != "client_id: must be a string" {
		t.Errorf("error = %q, want %q", err.Error(), "client_id: must be a string")
	}
}

func TestRedactConfig(t *testing.T) {
	raw := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "super-secret-value",
		"redirect_url":  "https://example.com/callback",
	}

	redacted := redactConfig(raw)

	// Check client_secret is redacted
	if redacted["client_secret"] != "***" {
		t.Errorf("redacted[client_secret] = %q, want %q", redacted["client_secret"], "***")
	}

	// Check other fields are preserved
	if redacted["client_id"] != "test-client-id" {
		t.Errorf("redacted[client_id] = %q, want %q", redacted["client_id"], "test-client-id")
	}
	if redacted["redirect_url"] != "https://example.com/callback" {
		t.Errorf("redacted[redirect_url] = %q, want %q", redacted["redirect_url"], "https://example.com/callback")
	}

	// Check original is not modified
	if raw["client_secret"] != "super-secret-value" {
		t.Error("redactConfig() modified the original map")
	}
}

func TestRedactConfig_NoSecret(t *testing.T) {
	raw := map[string]any{
		"client_id":    "test-client-id",
		"redirect_url": "https://example.com/callback",
	}

	redacted := redactConfig(raw)

	// Check fields are preserved
	if redacted["client_id"] != "test-client-id" {
		t.Errorf("redacted[client_id] = %q, want %q", redacted["client_id"], "test-client-id")
	}
	if redacted["redirect_url"] != "https://example.com/callback" {
		t.Errorf("redacted[redirect_url] = %q, want %q", redacted["redirect_url"], "https://example.com/callback")
	}
}

func TestIsConfigured(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{
			name: "fully configured",
			raw: map[string]any{
				"client_id":     "test-client-id",
				"client_secret": "test-secret",
			},
			want: true,
		},
		{
			name: "missing client_id",
			raw: map[string]any{
				"client_secret": "test-secret",
			},
			want: false,
		},
		{
			name: "missing client_secret",
			raw: map[string]any{
				"client_id": "test-client-id",
			},
			want: false,
		},
		{
			name: "empty client_id",
			raw: map[string]any{
				"client_id":     "",
				"client_secret": "test-secret",
			},
			want: false,
		},
		{
			name: "empty client_secret",
			raw: map[string]any{
				"client_id":     "test-client-id",
				"client_secret": "",
			},
			want: false,
		},
		{
			name: "invalid type",
			raw: map[string]any{
				"client_id": 123,
			},
			want: false,
		},
		{
			name: "empty map",
			raw:  map[string]any{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConfigured(tt.raw); got != tt.want {
				t.Errorf("isConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigured_Exported(t *testing.T) {
	raw := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "test-secret",
	}

	if !Configured(raw) {
		t.Error("Configured() should return true for valid config")
	}

	raw["client_id"] = ""
	if Configured(raw) {
		t.Error("Configured() should return false when client_id is empty")
	}
}
