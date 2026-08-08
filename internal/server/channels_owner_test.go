package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// channelOwnerEnv is an admin env plus a plain user who owns one agent. A
// channel is an agent's phone number, so this is the shape every assertion below
// cares about: what an ordinary agent owner may do to channels, and what stays
// invisible to them.
type channelOwnerEnv struct {
	*testEnv
	ownerToken   string
	ownerAgentID string
	adminAgentID string
}

func setupChannelOwner(t *testing.T) *channelOwnerEnv {
	t.Helper()
	env := setupAdmin(t)
	_, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "channelowner", auth.RoleUser)

	rr := doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/agents", config.Agent{
		Name:    "Owner Agent",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner create agent: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var ownerAgent config.Agent
	if err := json.Unmarshal(parseResponse(t, rr).Data, &ownerAgent); err != nil {
		t.Fatalf("decode owner agent: %v", err)
	}

	adminAgentID := createTestAgent(t, env, config.Agent{
		Name:    "Admin Agent",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
	})

	return &channelOwnerEnv{
		testEnv:      env,
		ownerToken:   ownerToken,
		ownerAgentID: ownerAgent.ID,
		adminAgentID: adminAgentID,
	}
}

func TestAgentOwnerManagesOwnChannels(t *testing.T) {
	env := setupChannelOwner(t)

	rr := doRequestWithSession(t, env.srv, env.ownerToken, http.MethodPost, "/api/channels", map[string]any{
		"type":     pkgchannel.PlatformTelegram,
		"agent_id": env.ownerAgentID,
		"config":   `{"token":"owner-token"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner create channel: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &created); err != nil {
		t.Fatalf("decode created channel: %v", err)
	}
	if created.AgentID != env.ownerAgentID {
		t.Fatalf("agent_id = %q, want %q", created.AgentID, env.ownerAgentID)
	}

	// Creating a channel must not write a deployment-wide plugin row: the
	// platform switch belongs to the admin, not to whoever adds a channel.
	if _, err := env.store.GetPlugin(context.Background(), config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram)); err == nil {
		overrides, listErr := env.store.ListPluginOverrides(context.Background())
		if listErr != nil {
			t.Fatalf("list plugin overrides: %v", listErr)
		}
		for _, override := range overrides {
			if override.ID == config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram) {
				t.Fatal("creating a channel wrote a channel plugin override row")
			}
		}
	}

	rr = doRequestWithSession(t, env.srv, env.ownerToken, http.MethodPatch, "/api/channels/"+created.ID, map[string]any{
		"enabled": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("owner update channel: status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, env.ownerToken, http.MethodDelete, "/api/channels/"+created.ID, nil)
	if rr.Code != http.StatusOK && rr.Code != http.StatusNoContent {
		t.Fatalf("owner delete channel: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAgentOwnerCannotReachForeignChannels(t *testing.T) {
	env := setupChannelOwner(t)

	rr := doRequest(t, env.testEnv, http.MethodPost, "/api/channels", map[string]any{
		"type":     pkgchannel.PlatformTelegram,
		"agent_id": env.adminAgentID,
		"config":   `{"token":"admin-token"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin create channel: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var foreign struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &foreign); err != nil {
		t.Fatalf("decode admin channel: %v", err)
	}

	// The owner's list holds only their own channels, and someone else's channel
	// is as opaque as a missing one on every single-resource path.
	rr = doRequestWithSession(t, env.srv, env.ownerToken, http.MethodGet, "/api/channels", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner list channels: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(parseListItems(t, rr, "channels"), &listed); err != nil {
		t.Fatalf("decode owner channel list: %v", err)
	}
	for _, ch := range listed {
		if ch.ID == foreign.ID {
			t.Fatalf("owner list leaked another agent's channel %q", foreign.ID)
		}
	}

	for _, tc := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPatch, map[string]any{"enabled": true}},
		{http.MethodDelete, nil},
	} {
		rr := doRequestWithSession(t, env.srv, env.ownerToken, tc.method, "/api/channels/"+foreign.ID, tc.body)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s foreign channel: status=%d, want 404 (body: %s)", tc.method, rr.Code, rr.Body.String())
		}
	}

	// An unbound channel has no owner but the deployment, so a non-admin cannot
	// mint one either.
	rr = doRequestWithSession(t, env.srv, env.ownerToken, http.MethodPost, "/api/channels", map[string]any{
		"type":   pkgchannel.PlatformDiscord,
		"config": `{"token":"orphan"}`,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("owner create unbound channel: status=%d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}
