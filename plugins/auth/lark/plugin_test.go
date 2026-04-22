package lark

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg == nil {
		t.Fatal("defaultConfig() returned nil")
	}

	expected := map[string]string{
		"app_id":       "",
		"app_secret":   "",
		"brand":        "lark",
		"redirect_url": "",
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
	if len(required) != 3 {
		t.Errorf("len(required) = %d, want 3", len(required))
	}

	// Check properties exist
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema[properties] not found or wrong type")
	}

	expectedProps := []string{"app_id", "app_secret", "brand", "redirect_url"}
	for _, prop := range expectedProps {
		if _, ok := properties[prop]; !ok {
			t.Errorf("schema missing property %q", prop)
		}
	}

	// Check brand enum
	brandProp, ok := properties["brand"].(map[string]any)
	if !ok {
		t.Fatal("brand property not found or wrong type")
	}
	enum, ok := brandProp["enum"].([]any)
	if !ok || len(enum) != 2 {
		t.Errorf("brand[enum] = %v, want 2 items", enum)
	}
}

func TestDecodeConfig_Valid(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want Config
	}{
		{
			name: "lark brand",
			raw: map[string]any{
				"app_id":       "test-app-id",
				"app_secret":   "test-secret",
				"brand":        "lark",
				"redirect_url": "https://example.com/callback",
			},
			want: Config{
				AppID:       "test-app-id",
				AppSecret:   "test-secret",
				Brand:       "lark",
				RedirectURL: "https://example.com/callback",
			},
		},
		{
			name: "feishu brand",
			raw: map[string]any{
				"app_id":       "test-app-id",
				"app_secret":   "test-secret",
				"brand":        "feishu",
				"redirect_url": "https://example.com/callback",
			},
			want: Config{
				AppID:       "test-app-id",
				AppSecret:   "test-secret",
				Brand:       "feishu",
				RedirectURL: "https://example.com/callback",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := decodeConfig(tt.raw)
			if err != nil {
				t.Fatalf("decodeConfig() error = %v", err)
			}
			if cfg != tt.want {
				t.Errorf("decodeConfig() = %+v, want %+v", cfg, tt.want)
			}
		})
	}
}

func TestDecodeConfig_Empty(t *testing.T) {
	raw := map[string]any{}

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}

	if cfg.AppID != "" || cfg.AppSecret != "" || cfg.Brand != "" || cfg.RedirectURL != "" {
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
			name: "app_id not string",
			raw:  map[string]any{"app_id": 123},
			want: "app_id",
		},
		{
			name: "app_secret not string",
			raw:  map[string]any{"app_secret": true},
			want: "app_secret",
		},
		{
			name: "brand not string",
			raw:  map[string]any{"brand": []string{}},
			want: "brand",
		},
		{
			name: "redirect_url not string",
			raw:  map[string]any{"redirect_url": 456.78},
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
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "lark brand",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
				"brand":      "lark",
			},
		},
		{
			name: "feishu brand",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
				"brand":      "feishu",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateConfig(tt.raw); err != nil {
				t.Errorf("validateConfig() error = %v", err)
			}
		})
	}
}

func TestValidateConfig_MissingAppID(t *testing.T) {
	raw := map[string]any{
		"app_secret": "test-secret",
		"brand":      "lark",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for missing app_id")
	}
	if err.Error() != "app_id: required" {
		t.Errorf("error = %q, want %q", err.Error(), "app_id: required")
	}
}

func TestValidateConfig_MissingAppSecret(t *testing.T) {
	raw := map[string]any{
		"app_id": "test-app-id",
		"brand":  "lark",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for missing app_secret")
	}
	if err.Error() != "app_secret: required" {
		t.Errorf("error = %q, want %q", err.Error(), "app_secret: required")
	}
}

func TestValidateConfig_EmptyAppID(t *testing.T) {
	raw := map[string]any{
		"app_id":     "",
		"app_secret": "test-secret",
		"brand":      "lark",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for empty app_id")
	}
	if err.Error() != "app_id: required" {
		t.Errorf("error = %q, want %q", err.Error(), "app_id: required")
	}
}

func TestValidateConfig_MissingBrand(t *testing.T) {
	raw := map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for missing brand")
	}
	if err.Error() != "brand: required" {
		t.Errorf("error = %q, want %q", err.Error(), "brand: required")
	}
}

func TestValidateConfig_EmptyBrand(t *testing.T) {
	raw := map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"brand":      "",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for empty brand")
	}
	if err.Error() != "brand: required" {
		t.Errorf("error = %q, want %q", err.Error(), "brand: required")
	}
}

func TestValidateConfig_InvalidBrand(t *testing.T) {
	raw := map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"brand":      "invalid-brand",
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error for invalid brand")
	}
	want := "brand: must be one of \"lark\" or \"feishu\""
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestValidateConfig_InvalidDecode(t *testing.T) {
	raw := map[string]any{
		"app_id": 123,
	}

	err := validateConfig(raw)
	if err == nil {
		t.Fatal("validateConfig() expected error")
	}
	if err.Error() != "app_id: must be a string" {
		t.Errorf("error = %q, want %q", err.Error(), "app_id: must be a string")
	}
}

func TestRedactConfig(t *testing.T) {
	raw := map[string]any{
		"app_id":       "test-app-id",
		"app_secret":   "super-secret-value",
		"brand":        "lark",
		"redirect_url": "https://example.com/callback",
	}

	redacted := redactConfig(raw)

	// Check app_secret is redacted
	if redacted["app_secret"] != "***" {
		t.Errorf("redacted[app_secret] = %q, want %q", redacted["app_secret"], "***")
	}

	// Check other fields are preserved
	if redacted["app_id"] != "test-app-id" {
		t.Errorf("redacted[app_id] = %q, want %q", redacted["app_id"], "test-app-id")
	}
	if redacted["brand"] != "lark" {
		t.Errorf("redacted[brand] = %q, want %q", redacted["brand"], "lark")
	}
	if redacted["redirect_url"] != "https://example.com/callback" {
		t.Errorf("redacted[redirect_url] = %q, want %q", redacted["redirect_url"], "https://example.com/callback")
	}

	// Check original is not modified
	if raw["app_secret"] != "super-secret-value" {
		t.Error("redactConfig() modified the original map")
	}
}

func TestRedactConfig_NoSecret(t *testing.T) {
	raw := map[string]any{
		"app_id":       "test-app-id",
		"brand":        "feishu",
		"redirect_url": "https://example.com/callback",
	}

	redacted := redactConfig(raw)

	// Check fields are preserved
	if redacted["app_id"] != "test-app-id" {
		t.Errorf("redacted[app_id] = %q, want %q", redacted["app_id"], "test-app-id")
	}
	if redacted["brand"] != "feishu" {
		t.Errorf("redacted[brand] = %q, want %q", redacted["brand"], "feishu")
	}
}

func TestIsConfigured(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{
			name: "fully configured lark",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
				"brand":      "lark",
			},
			want: true,
		},
		{
			name: "fully configured feishu",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
				"brand":      "feishu",
			},
			want: true,
		},
		{
			name: "missing app_id",
			raw: map[string]any{
				"app_secret": "test-secret",
				"brand":      "lark",
			},
			want: false,
		},
		{
			name: "missing app_secret",
			raw: map[string]any{
				"app_id": "test-app-id",
				"brand":  "lark",
			},
			want: false,
		},
		{
			name: "missing brand",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
			},
			want: false,
		},
		{
			name: "empty app_id",
			raw: map[string]any{
				"app_id":     "",
				"app_secret": "test-secret",
				"brand":      "lark",
			},
			want: false,
		},
		{
			name: "empty brand",
			raw: map[string]any{
				"app_id":     "test-app-id",
				"app_secret": "test-secret",
				"brand":      "",
			},
			want: false,
		},
		{
			name: "invalid type",
			raw: map[string]any{
				"app_id": 123,
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
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"brand":      "lark",
	}

	if !Configured(raw) {
		t.Error("Configured() should return true for valid config")
	}

	raw["app_id"] = ""
	if Configured(raw) {
		t.Error("Configured() should return false when app_id is empty")
	}
}
