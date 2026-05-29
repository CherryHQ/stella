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

func TestLoadBuiltinLarkCLIOAuthProvider(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	for _, p := range m.Plugins {
		if p.ID != "tool/lark-cli" {
			continue
		}
		if p.OAuthProvider != "lark" {
			t.Fatalf("OAuthProvider = %q, want lark", p.OAuthProvider)
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
