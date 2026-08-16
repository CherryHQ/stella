package manifestplugins

import (
	"slices"
	"testing"
)

func TestLoadBuiltin(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	if len(m.Plugins) == 0 {
		t.Fatal("LoadBuiltin() returned empty plugins")
	}
}

func TestLoadBuiltinTapWebBundlesMatchingAgentBrowser(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	for _, p := range m.Plugins {
		if p.ID != "tool/tap-web" {
			continue
		}
		want := []ManifestBinary{
			{Name: "tap", Tool: "github:vaayne/tap", Version: "1.1.0"},
			{Name: "agent-browser", Tool: "github:vercel-labs/agent-browser", Version: "0.33.2"},
			{Name: "lightpanda", Tool: "github:lightpanda-io/browser", Version: "nightly"},
		}
		if !slices.EqualFunc(p.Binaries, want, func(got, want ManifestBinary) bool {
			return got.Name == want.Name && got.Tool == want.Tool && got.Version == want.Version
		}) {
			t.Fatalf("Binaries = %#v, want Tap 1.1.0 with matching agent-browser 0.33.2", p.Binaries)
		}
		return
	}
	t.Fatal("tool/tap-web not found")
}

func TestLoadBuiltinLarkCLIUsesManagedFeishuOAuth(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	for _, p := range m.Plugins {
		if p.ID != "tool/lark-cli" {
			continue
		}
		if p.OAuthProvider != "feishu" {
			t.Fatalf("OAuthProvider = %q, want feishu", p.OAuthProvider)
		}
		if len(p.SessionEnvs) != 3 {
			t.Fatalf("SessionEnvs = %#v, want token, app ID, and brand injection", p.SessionEnvs)
		}
		if len(p.Binaries) != 1 || p.Binaries[0].Version != "1.0.87" {
			t.Fatalf("Binaries = %#v, want pinned lark-cli 1.0.87", p.Binaries)
		}
		return
	}
	t.Fatal("tool/lark-cli not found")
}

func TestLoadBuiltinLarkProvidersRecommendFullCLIScopes(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	found := map[string]bool{}
	for _, provider := range m.OAuthProviders {
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
