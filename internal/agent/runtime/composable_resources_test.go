package runtime

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestAppendCLIResourcesCombinesOAuthDeclarations(t *testing.T) {
	view := pkgplugins.SessionPluginView{}
	identity := pkgplugins.PluginResourceIdentity{PluginID: "combined", ConfigID: "parent", Scope: "user", Revision: 9}
	appendCLIResources(&view, identity, plugin.ResourcePayload{
		Binaries:    []plugin.BinaryResource{{Name: "demo", Tool: "github:example/demo"}},
		SessionEnvs: []plugin.SessionEnvResource{{EnvVar: "DEMO_TOKEN", Source: "oauth.access_token", Required: true}},
		OAuth: []plugin.OAuthRequirement{{
			Provider: "demo", Scopes: []string{"read"},
			Bindings: []plugin.OAuthBinding{
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
