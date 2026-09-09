package oauth

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/resources"
)

func TestParseBuiltinProviders(t *testing.T) {
	providers, err := ParseProviders(resources.BuiltinOAuthYAML())
	if err != nil {
		t.Fatalf("ParseProviders() error: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("ParseProviders() returned no providers")
	}

	found := map[string]bool{}
	for _, provider := range providers {
		if provider.ID != "lark" && provider.ID != "feishu" {
			continue
		}
		found[provider.ID] = true
		// The builtin default is the recommended lark-cli capability surface, so
		// one authorization covers every documented command. Admins trim it; it
		// is a floor users can still grow incrementally.
		if len(provider.Scopes) < 100 {
			t.Fatalf("%s defaults = %d scopes, want the full lark-cli capability set", provider.ID, len(provider.Scopes))
		}
		for _, want := range []string{"offline_access", "contact:user.basic_profile:readonly"} {
			if !slices.Contains(provider.Scopes, want) {
				t.Fatalf("%s defaults missing %q", provider.ID, want)
			}
		}
	}
	if !found["lark"] || !found["feishu"] {
		t.Fatalf("providers found = %v, want lark and feishu", found)
	}
}

func TestProviderIDs(t *testing.T) {
	ids, err := ProviderIDs(resources.BuiltinOAuthYAML())
	if err != nil {
		t.Fatalf("ProviderIDs() error: %v", err)
	}
	for _, want := range []string{"github", "lark", "x", "feishu"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("ProviderIDs() = %v, missing %q", ids, want)
		}
	}
}

func TestNewBuiltinRegistryConvertsAuthStyle(t *testing.T) {
	registry, err := NewBuiltinRegistry([]byte(`oauth_providers:
  - id: params
    vault_key: PARAMS
    flows:
      - type: device_code
        device_auth_url: https://example.test/device
        token_url: https://example.test/token
        auth_style: in_params
  - id: header
    vault_key: HEADER
    flows:
      - type: authorization_code
        auth_url: https://example.test/authorize
        token_url: https://example.test/token
        auth_style: in_header
  - id: auto
    vault_key: AUTO
    flows:
      - type: device_code
        device_auth_url: https://example.test/device
        token_url: https://example.test/token
        auth_style: future_value
`))
	if err != nil {
		t.Fatalf("NewBuiltinRegistry() error: %v", err)
	}

	tests := []struct {
		provider string
		want     oauth2.AuthStyle
	}{
		{provider: "params", want: oauth2.AuthStyleInParams},
		{provider: "header", want: oauth2.AuthStyleInHeader},
		{provider: "auto", want: oauth2.AuthStyleAutoDetect},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			config, ok := registry.Get(test.provider)
			if !ok || len(config.Flows) != 1 {
				t.Fatalf("registry.Get(%q) = %#v, ok=%v", test.provider, config, ok)
			}
			if config.Flows[0].AuthStyle != test.want {
				t.Errorf("AuthStyle = %v, want %v", config.Flows[0].AuthStyle, test.want)
			}
		})
	}
}

func TestParseProvidersRejectsInvalidDefinitions(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing id",
			yaml: "oauth_providers:\n  - vault_key: TEST\n    flows:\n      - type: device_code\n        device_auth_url: https://example.test/device\n        token_url: https://example.test/token\n",
			want: "id is required",
		},
		{
			name: "duplicate id",
			yaml: "oauth_providers:\n  - id: test\n    vault_key: TEST\n    flows:\n      - type: device_code\n        device_auth_url: https://example.test/device\n        token_url: https://example.test/token\n  - id: test\n    vault_key: TEST2\n    flows:\n      - type: device_code\n        device_auth_url: https://example.test/device\n        token_url: https://example.test/token\n",
			want: "duplicate id",
		},
		{
			name: "missing authorization endpoint",
			yaml: "oauth_providers:\n  - id: test\n    vault_key: TEST\n    flows:\n      - type: authorization_code\n        token_url: https://example.test/token\n",
			want: "auth_url is required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseProviders([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseProviders() error = %v, want %q", err, test.want)
			}
		})
	}
}
