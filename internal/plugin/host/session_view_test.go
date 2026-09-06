package host

import (
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestSelectedResourceIdentityRequiresSelectedConfig(t *testing.T) {
	definition := plugin.Definition{ID: "tool/demo"}
	if _, err := selectedResourceIdentity(definition, plugin.ResolvedPlugin{}); err == nil {
		t.Fatal("selectedResourceIdentity accepted an enabled plugin without a selected config")
	}

	enabled := true
	resolved := plugin.ResolvedPlugin{Config: &plugin.Config{
		ID:       "config/user-agent",
		Scope:    plugin.ScopeUserAgent,
		Revision: 7,
		Enabled:  &enabled,
	}}
	identity, err := selectedResourceIdentity(definition, resolved)
	if err != nil {
		t.Fatalf("selectedResourceIdentity: %v", err)
	}
	want := pkgplugins.PluginResourceIdentity{PluginID: "tool/demo", ConfigID: "config/user-agent", Scope: "user_agent", Revision: 7}
	if identity != want {
		t.Fatalf("identity = %+v, want %+v", identity, want)
	}
}

func TestAppendCLIResourcesCarriesIdentityAndClonesOptions(t *testing.T) {
	options := map[string]any{"extras": "x"}
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "tool/demo", ConfigID: "cfg-1", Scope: "user", Revision: 3}
	appendCLIResources(&view, identity, manifest.CLIPayload{
		Binaries: []manifest.ManifestBinary{{Name: "demo", Tool: "github:demo/demo", Version: "1.2.3", Options: options}},
		Skills:   []manifest.ManifestSkill{{Repo: "owner/repo", Name: "demo"}},
		SessionEnvs: []manifest.ManifestSessionEnv{{
			EnvVar: "DEMO_TOKEN", Source: "oauth", Required: true,
		}},
		OAuthProvider: "demo-oauth",
	})

	options["extras"] = "mutated"
	if len(view.BinarySpecs) != 1 || view.BinarySpecs[0].Options["extras"] != "x" {
		t.Fatalf("binary options were not cloned: %+v", view.BinarySpecs)
	}
	if got := view.BinarySpecs[0].PluginResourceIdentity; got != identity {
		t.Fatalf("binary identity = %+v, want %+v", got, identity)
	}
	if len(view.SkillSpecs) != 1 || view.SkillSpecs[0].PluginResourceIdentity != identity {
		t.Fatalf("skill projection = %+v, want identity %+v", view.SkillSpecs, identity)
	}
	if len(view.SessionEnvSpecs) != 1 {
		t.Fatalf("session env projection = %+v, want one entry", view.SessionEnvSpecs)
	}
	env := view.SessionEnvSpecs[0]
	if env.PluginID != identity.PluginID || env.ConfigID != identity.ConfigID || env.Scope != identity.Scope || env.Revision != identity.Revision || env.OAuthProviderID != "demo-oauth" {
		t.Fatalf("session env identity/provider = %+v, want identity %+v and provider demo-oauth", env, identity)
	}
}

func TestBuiltinBundledRuntimeDeclarationProjectsBinaryAndSkillTogether(t *testing.T) {
	definitions, err := BuiltinBundledRuntimeDefinitions()
	if err != nil {
		t.Fatalf("BuiltinBundledRuntimeDefinitions: %v", err)
	}
	if len(definitions) != 1 || definitions[0].ID != "tool/xberg" || definitions[0].Backend != plugin.BackendGo || definitions[0].ImplementationKey != "tool/xberg" {
		t.Fatalf("bundled runtime definitions = %+v", definitions)
	}
	resources := BuiltinBundledRuntimeResources()
	if len(resources) != 1 || resources[0].BinaryName != "xberg" || resources[0].SkillName != "xberg" {
		t.Fatalf("bundled runtime resources = %+v", resources)
	}
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "tool/xberg", ConfigID: "cfg-xberg", Scope: string(plugin.ScopeSystem), Revision: 2}
	appendBundledRuntimeResources(&view, identity, resources[0])
	if len(view.BundledBinarySpecs) != 1 || view.BundledBinarySpecs[0].Name != "xberg" || view.BundledBinarySpecs[0].PluginResourceIdentity != identity {
		t.Fatalf("bundled binary projection = %+v", view.BundledBinarySpecs)
	}
	if len(view.SkillSpecs) != 1 || view.SkillSpecs[0].Name != "xberg" || view.SkillSpecs[0].PluginResourceIdentity != identity {
		t.Fatalf("bundled skill projection = %+v", view.SkillSpecs)
	}
}

func TestValidateResolvedCLIPayloadRejectsIncompleteCapabilityLift(t *testing.T) {
	definition := plugin.Definition{
		ID: "tool/demo", Namespace: "demo", DisplayName: "Demo",
		Backend: plugin.BackendCLI, Source: plugin.SourceBuiltin,
		ImplementationKey: "tool/demo", DefaultEnabled: false, Revision: 1,
		Spec: json.RawMessage(`{"binaries":[{"name":"demo","tool":"github:owner/demo","version":"1.0.0"}]}`),
	}
	disabled := false
	resolved := plugin.ResolvedPlugin{
		Config: &plugin.Config{
			ID: "cfg-user", PluginID: definition.ID, Namespace: definition.Namespace,
			Scope: plugin.ScopeUser, UserID: "user-1", Enabled: &disabled, Revision: 1,
			Payload: json.RawMessage(`{"binaries":null}`),
		},
		Effective: plugin.Effective{Payload: json.RawMessage(`{}`)},
	}
	if err := validateResolvedCLIPayload(definition, resolved); err == nil {
		t.Fatal("validateResolvedCLIPayload accepted an incomplete payload after capability lift")
	}
}
