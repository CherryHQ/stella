package mcp

import "testing"

func TestManagementProjectionRedactsLegacyEndpoint(t *testing.T) {
	view := managementProjection(Registration{
		ID: "legacy", Scope: ScopeUser, Name: "legacy",
		URL:       "https://user:token@example.test/mcp?access_token=leaked#fragment",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer,
	})
	if view.URL != "" || !view.EndpointRedacted {
		t.Fatalf("legacy endpoint projection = %#v, want redacted", view)
	}
}
