package channel

import (
	"context"
	"testing"
)

func TestGroupRosterPromptLoaderIncludesGroupContext(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	roster := NewGroupRosterPromptLoader(fx.db)(context.Background(), fx.groupID, "agent-1")
	if roster.Platform != "web" || roster.GroupName != "Group One" || roster.SelfName != "Agent One" {
		t.Fatalf("roster = %#v, want web/Group One/Agent One", roster)
	}
	if len(roster.PeerNames) != 0 {
		t.Fatalf("peer names = %#v, want none", roster.PeerNames)
	}

	missing := NewGroupRosterPromptLoader(fx.db)(context.Background(), "00000000-0000-0000-0000-000000000000", "agent-1")
	if missing.Platform != "" || missing.GroupName != "" {
		t.Fatalf("missing group context = %#v, want empty fields", missing)
	}
}
