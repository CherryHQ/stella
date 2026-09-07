package runtime

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestAppendCLIResourcesCombinesOAuthDeclarations(t *testing.T) {
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "combined", ConfigID: "parent", Scope: "user", Revision: 9}
	appendCLIResources(&view, identity, manifest.CLIPayload{
		Binaries:    []manifest.ManifestBinary{{Name: "demo", Tool: "github:example/demo"}},
		SessionEnvs: []manifest.ManifestSessionEnv{{EnvVar: "DEMO_TOKEN", Source: "oauth.access_token", Required: true}},
		OAuth: []manifest.ManifestOAuthRequirement{{
			Provider: "demo", Scopes: []string{"read"},
			Bindings: []manifest.ManifestOAuthBinding{
				{Credential: "access_token", EnvVar: "DEMO_TOKEN"},
			},
		}},
	})
	if len(view.BinarySpecs) != 1 || len(view.SessionEnvSpecs) != 1 {
		t.Fatalf("resource projection contains missing binaries or duplicate env bindings: %+v", view)
	}
	env := view.SessionEnvSpecs[0]
	if env.ConfigID != identity.ConfigID || env.Revision != 9 || env.OAuthProviderID != "demo" || !env.Required || len(env.OAuthScopes) != 1 || env.OAuthScopes[0] != "read" {
		t.Fatalf("env identity, requirement, or provider binding was lost: %+v", env)
	}
}
