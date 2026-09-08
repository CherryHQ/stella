package plugin

import "testing"

func TestOAuthPreviewChangesReportsPermissionAndBindingDiffs(t *testing.T) {
	changes := oauthPreviewChanges(
		[]OAuthRequirement{{Provider: "github", Scopes: []string{"read"}, Bindings: []OAuthBinding{{Credential: "access_token", EnvVar: "GITHUB_TOKEN"}}}},
		[]OAuthRequirement{{Provider: "github", Scopes: []string{"read", "write"}, Bindings: []OAuthBinding{{Credential: "access_token", EnvVar: "GH_TOKEN"}}}},
	)
	if len(changes) != 1 || len(changes[0].AddedScopes) != 1 || changes[0].AddedScopes[0] != "write" {
		t.Fatalf("scope diff = %#v", changes)
	}
	if len(changes[0].AddedBindings) != 1 || len(changes[0].RemovedBindings) != 1 {
		t.Fatalf("binding diff = %#v", changes[0])
	}
}
