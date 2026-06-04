package channel

import (
	"context"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestArbiterMentionBypass(t *testing.T) {
	a := NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3})
	ctx := context.Background()

	mentions := []pkgchannel.Mention{
		{PlatformID: "bot1", AgentID: "agent-1"},
	}
	members := []GroupMember{
		{AgentID: "agent-1"},
		{AgentID: "agent-2"},
	}

	d := a.Decide(ctx, "g1", mentions, members, "agent-2")
	if d.Debounced {
		t.Fatal("should not be debounced")
	}
	if len(d.RespondingAgents) != 1 || d.RespondingAgents[0] != "agent-1" {
		t.Fatalf("expected [agent-1], got %v", d.RespondingAgents)
	}
}

func TestArbiterNoMentionFallback(t *testing.T) {
	a := NewArbiter(ArbiterConfig{})
	ctx := context.Background()

	d := a.Decide(ctx, "g1", nil, []GroupMember{{AgentID: "agent-1"}}, "agent-default")
	if len(d.RespondingAgents) != 1 || d.RespondingAgents[0] != "agent-default" {
		t.Fatalf("expected [agent-default], got %v", d.RespondingAgents)
	}
}

func TestArbiterMaxReplies(t *testing.T) {
	a := NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 1})
	ctx := context.Background()

	mentions := []pkgchannel.Mention{
		{PlatformID: "bot1", AgentID: "agent-1"},
		{PlatformID: "bot2", AgentID: "agent-2"},
	}
	members := []GroupMember{
		{AgentID: "agent-1"},
		{AgentID: "agent-2"},
	}

	d := a.Decide(ctx, "g1", mentions, members, "")
	if len(d.RespondingAgents) != 1 {
		t.Fatalf("expected max 1 response, got %d: %v", len(d.RespondingAgents), d.RespondingAgents)
	}
}

func TestArbiterDebounce(t *testing.T) {
	a := NewArbiter(ArbiterConfig{DebounceWindow: 500 * time.Millisecond})
	ctx := context.Background()

	members := []GroupMember{{AgentID: "agent-1"}}

	d1 := a.Decide(ctx, "g1", nil, members, "agent-1")
	if d1.Debounced {
		t.Fatal("first call should not be debounced")
	}
	if len(d1.RespondingAgents) != 1 {
		t.Fatalf("expected 1 agent, got %v", d1.RespondingAgents)
	}

	d2 := a.Decide(ctx, "g1", nil, members, "agent-1")
	if !d2.Debounced {
		t.Fatal("second call within window should be debounced")
	}

	// Different group should not be debounced.
	d3 := a.Decide(ctx, "g2", nil, members, "agent-1")
	if d3.Debounced {
		t.Fatal("different group should not be debounced")
	}
}

func TestArbiterMentionedNonMemberIgnored(t *testing.T) {
	a := NewArbiter(ArbiterConfig{})
	ctx := context.Background()

	mentions := []pkgchannel.Mention{
		{PlatformID: "bot1", AgentID: "agent-not-member"},
	}
	members := []GroupMember{
		{AgentID: "agent-1"},
	}

	d := a.Decide(ctx, "g1", mentions, members, "agent-1")
	if len(d.RespondingAgents) != 1 || d.RespondingAgents[0] != "agent-1" {
		t.Fatalf("expected fallback to [agent-1], got %v", d.RespondingAgents)
	}
}

func TestArbiterUnresolvedMentionFallback(t *testing.T) {
	a := NewArbiter(ArbiterConfig{})
	ctx := context.Background()

	mentions := []pkgchannel.Mention{
		{PlatformID: "unknown_bot"},
	}
	members := []GroupMember{{AgentID: "agent-1"}}

	d := a.Decide(ctx, "g1", mentions, members, "agent-1")
	if len(d.RespondingAgents) != 1 || d.RespondingAgents[0] != "agent-1" {
		t.Fatalf("unresolved mention should fall back to default: got %v", d.RespondingAgents)
	}
}
