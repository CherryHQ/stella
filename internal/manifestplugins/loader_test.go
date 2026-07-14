package manifestplugins

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/resources"
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

func TestLarkCLIReconnectGuidanceMatchesOAuthProvider(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}

	var larkCLI *ManifestPlugin
	for i := range m.Plugins {
		if m.Plugins[i].ID == "tool/lark-cli" {
			larkCLI = &m.Plugins[i]
			break
		}
	}
	if larkCLI == nil {
		t.Fatal("tool/lark-cli not found")
	}

	provider := larkCLI.OAuthProvider
	if provider == "" {
		t.Fatal("tool/lark-cli OAuthProvider is empty")
	}
	if !strings.Contains(larkCLI.Prompt, "stella oauth connect "+provider) {
		t.Errorf("tool/lark-cli prompt must reconnect %q", provider)
	}

	providerParameter := regexp.MustCompile(`provider=([a-z0-9_-]+)`)
	guidanceCount := 0
	err = fs.WalkDir(resources.FS(), "skills/system/lark-cli", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := fs.ReadFile(resources.FS(), path)
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(contents), "\n") {
			if !strings.Contains(line, "oauth connect") {
				continue
			}
			for _, match := range providerParameter.FindAllStringSubmatch(line, -1) {
				guidanceCount++
				if match[1] != provider {
					t.Errorf("%s reconnects provider %q, want manifest binding %q", path, match[1], provider)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk bundled lark-cli guidance: %v", err)
	}
	if guidanceCount == 0 {
		t.Fatal("bundled lark-cli guidance has no provider= reconnect instructions")
	}
}
