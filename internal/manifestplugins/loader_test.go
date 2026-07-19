package manifestplugins

import "testing"

func TestLoadBuiltin(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	if len(m.Plugins) == 0 {
		t.Fatal("LoadBuiltin() returned empty plugins")
	}
}

func TestLoadBuiltinXberg(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}

	// The historical ID preserves persisted plugin overrides and install state.
	const pluginID = "tool/kreuzberg"
	for _, p := range m.Plugins {
		if p.ID != pluginID {
			continue
		}
		if p.Name != "xberg" || p.DisplayName != "Xberg" {
			t.Fatalf("plugin identity = (%q, %q), want (xberg, Xberg)", p.Name, p.DisplayName)
		}
		if len(p.Binaries) != 1 {
			t.Fatalf("len(Binaries) = %d, want 1", len(p.Binaries))
		}
		b := p.Binaries[0]
		if b.Name != "xberg" || b.Tool != "github:xberg-io/xberg" || b.Version != "1.0.0-rc.29" {
			t.Fatalf("binary = %+v, want Xberg v1.0.0-rc.29", b)
		}
		platforms, ok := b.Options["platforms"].(map[string]any)
		if !ok {
			t.Fatalf("platforms = %#v, want map", b.Options["platforms"])
		}
		for platform, asset := range map[string]string{
			"linux-arm64": "xberg-cli-aarch64-unknown-linux-gnu.tar.gz",
			"linux-x64":   "xberg-cli-x86_64-unknown-linux-gnu.tar.gz",
			"macos-arm64": "xberg-cli-aarch64-apple-darwin.tar.gz",
			"windows-x64": "xberg-cli-x86_64-pc-windows-msvc.zip",
		} {
			options, ok := platforms[platform].(map[string]any)
			if !ok || options["asset_pattern"] != asset {
				t.Errorf("platform %q = %#v, want asset_pattern %q", platform, platforms[platform], asset)
			}
		}
		if _, ok := b.Options["rename_exe"]; ok {
			t.Fatal("Xberg must not expose a legacy executable alias")
		}
		return
	}
	t.Fatal("Xberg plugin not found")
}

func TestLoadBuiltinLarkCLIOAuthProvider(t *testing.T) {
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
		for _, se := range p.SessionEnvs {
			if se.EnvVar == "LARKSUITE_CLI_BRAND" && se.Source != "oauth.brand" {
				t.Fatalf("LARKSUITE_CLI_BRAND source = %q, want oauth.brand", se.Source)
			}
		}
		return
	}
	t.Fatal("tool/lark-cli not found")
}
