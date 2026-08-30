package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

// The agent list is readable by every user for a system-scope agent, so the
// projection is the one place a user id could leak into an ordinary user's
// hands. can_manage exists so no client has to ask for that id.
func TestAgentToAPIRevealsTheCreatorOnlyToAManager(t *testing.T) {
	agent := config.Agent{ID: "a1", Name: "A1", CreatorID: "owner", SystemSettingsToolsEnabled: true}
	cases := []struct {
		name          string
		viewer        agentViewer
		wantManage    bool
		wantCreatorID string // "" means the field must be absent
	}{
		{
			name:          "the creator manages and sees themselves",
			viewer:        agentViewer{userID: "owner"},
			wantManage:    true,
			wantCreatorID: "owner",
		},
		{
			name:          "an admin manages any agent and sees its creator",
			viewer:        agentViewer{userID: "root", isAdmin: true},
			wantManage:    true,
			wantCreatorID: "owner",
		},
		{
			name:       "another user learns neither",
			viewer:     agentViewer{userID: "stranger"},
			wantManage: false,
		},
		{
			name:       "an unauthenticated projection reveals nothing",
			viewer:     agentViewer{},
			wantManage: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := agentToAPI(agent, tc.viewer)
			if out.CanManage == nil || *out.CanManage != tc.wantManage {
				t.Fatalf("can_manage = %v, want %v", out.CanManage, tc.wantManage)
			}
			switch {
			case tc.wantCreatorID == "":
				if out.CreatorId != nil {
					t.Errorf("creator_id leaked %q", *out.CreatorId)
				}
				if out.SystemSettingsToolsEnabled != nil {
					t.Errorf("system_settings_tools_enabled leaked %t", *out.SystemSettingsToolsEnabled)
				}
			case out.CreatorId == nil || *out.CreatorId != tc.wantCreatorID:
				t.Errorf("creator_id = %v, want %q", out.CreatorId, tc.wantCreatorID)
			case out.SystemSettingsToolsEnabled == nil || !*out.SystemSettingsToolsEnabled:
				t.Errorf("system_settings_tools_enabled = %v, want true", out.SystemSettingsToolsEnabled)
			}
		})
	}
}

// A creatorless agent (seeded, or created before creators were recorded) must
// not turn every user into its manager through an empty-string match.
func TestAgentToAPIDoesNotMatchAnEmptyCreator(t *testing.T) {
	out := agentToAPI(config.Agent{ID: "seeded"}, agentViewer{userID: ""})
	if out.CanManage == nil || *out.CanManage {
		t.Fatal("an empty creator must not match an empty viewer")
	}
}
