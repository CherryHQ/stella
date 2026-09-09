package runtime

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestAppendCLIResourcesCarriesIdentityAndClonesOptions(t *testing.T) {
	options := map[string]any{"extras": "x"}
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "demo", ConfigID: "cfg-1", Scope: "user", Revision: 3}
	appendCLIResources(&view, identity, plugin.ResourcePayload{
		ContentDigest: "sha256:package-a",
		Binaries:      []plugin.BinaryResource{{Name: "demo", Tool: "github:demo/demo", Version: "1.2.3", Options: options}},
		Skills:        []plugin.SkillResource{{Name: "demo"}},
		SessionEnvs: []plugin.SessionEnvResource{{
			EnvVar: "DEMO_TOKEN", Source: "oauth.access_token", Required: true,
		}},
		OAuth: []plugin.OAuthRequirement{{Provider: "demo-oauth", Bindings: []plugin.OAuthBinding{{Credential: "access_token", EnvVar: "DEMO_TOKEN"}}}},
	})

	options["extras"] = "mutated"
	if len(view.BinarySpecs) != 1 || view.BinarySpecs[0].Options["extras"] != "x" {
		t.Fatalf("binary options were not cloned: %+v", view.BinarySpecs)
	}
	if got := view.BinarySpecs[0].PluginResourceIdentity; got != identity {
		t.Fatalf("binary identity = %+v, want %+v", got, identity)
	}
	if view.BinarySpecs[0].PackageDigest != "sha256:package-a" {
		t.Fatalf("binary package digest = %q, want package content digest", view.BinarySpecs[0].PackageDigest)
	}
	if len(view.SessionEnvSpecs) != 1 {
		t.Fatalf("session env projection = %+v, want one entry", view.SessionEnvSpecs)
	}
	env := view.SessionEnvSpecs[0]
	if env.PluginID != identity.PluginID || env.ConfigID != identity.ConfigID || env.Scope != identity.Scope || env.Revision != identity.Revision || env.OAuthProviderID != "demo-oauth" {
		t.Fatalf("session env identity/provider = %+v, want identity %+v and provider demo-oauth", env, identity)
	}
}

func TestAppendSkillResourcesCarriesPackageIdentityAndPath(t *testing.T) {
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "demo", ConfigID: "cfg-1", Scope: "user", Revision: 3}
	appendSkillResources(&view, identity, plugin.ResourcePayload{
		ContentDigest: "sha256:package-a",
		Skills:        []plugin.SkillResource{{Name: "docs", Path: "skills/docs/SKILL.md", Description: "documentation"}},
	}, false)
	if len(view.SkillSpecs) != 1 {
		t.Fatalf("skill projection = %+v", view.SkillSpecs)
	}
	got := view.SkillSpecs[0]
	if got.PluginID != identity.PluginID || got.ConfigID != identity.ConfigID || got.PackageDigest != "sha256:package-a" || got.Path != "skills/docs/SKILL.md" || got.Description != "documentation" {
		t.Fatalf("skill projection = %+v", got)
	}
}

func TestAppendSkillResourcesUsesPublishedAssetDigest(t *testing.T) {
	view := pkgplugins.SessionPluginView{}
	appendSkillResources(&view, pkgplugins.PluginResourceIdentity{PluginID: "demo", ConfigID: "cfg-1", Scope: "user", Revision: 3}, plugin.ResourcePayload{
		ContentDigest: "sha256:definition",
		Content:       &plugin.ContentReference{Digest: "sha256:assets"},
		Skills:        []plugin.SkillResource{{Name: "docs"}},
	}, false)
	if got := view.SkillSpecs[0].PackageDigest; got != "sha256:assets" {
		t.Fatalf("skill package digest = %q, want published asset digest", got)
	}
}
