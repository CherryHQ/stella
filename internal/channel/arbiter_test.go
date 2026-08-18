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

func TestArbiterUsesDurablePerGroupCap(t *testing.T) {
	a := NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 1})
	members := []GroupMember{{AgentID: "a"}, {AgentID: "b"}, {AgentID: "c"}}

	got := a.Decide(context.Background(), "group", nil, members, "", DecideOptions{
		AllMembersFallback:   true,
		MaxRepliesPerTrigger: 2,
	})
	if len(got.RespondingAgents) != 2 || got.RespondingAgents[0] != "a" || got.RespondingAgents[1] != "b" {
		t.Fatalf("responders = %v, want [a b]", got.RespondingAgents)
	}
}

func TestArbiterMentionedAgentsBypassMaxReplies(t *testing.T) {
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
	if len(d.RespondingAgents) != 2 || d.RespondingAgents[0] != "agent-1" || d.RespondingAgents[1] != "agent-2" {
		t.Fatalf("expected both mentioned agents, got %v", d.RespondingAgents)
	}
}

func TestArbiterFallbackMaxReplies(t *testing.T) {
	a := NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 1})
	ctx := context.Background()
	members := []GroupMember{
		{AgentID: "agent-1"},
		{AgentID: "agent-2"},
	}

	d := a.Decide(ctx, "g1", nil, members, "", DecideOptions{AllMembersFallback: true})
	if len(d.RespondingAgents) != 1 || d.RespondingAgents[0] != "agent-1" {
		t.Fatalf("expected capped fallback [agent-1], got %v", d.RespondingAgents)
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

func TestArbiterMentionBypassesDebounce(t *testing.T) {
	a := NewArbiter(ArbiterConfig{DebounceWindow: 500 * time.Millisecond})
	ctx := context.Background()

	members := []GroupMember{{AgentID: "agent-1"}}

	// First call: no mention, triggers default.
	d1 := a.Decide(ctx, "g1", nil, members, "agent-1")
	if d1.Debounced {
		t.Fatal("first call should not be debounced")
	}

	// Second call within window WITH @mention: must NOT be debounced.
	mentions := []pkgchannel.Mention{{PlatformID: "bot1", AgentID: "agent-1"}}
	d2 := a.Decide(ctx, "g1", mentions, members, "agent-1")
	if d2.Debounced {
		t.Fatal("explicit @mention within debounce window should NOT be debounced")
	}
	if len(d2.RespondingAgents) != 1 || d2.RespondingAgents[0] != "agent-1" {
		t.Fatalf("expected [agent-1], got %v", d2.RespondingAgents)
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

func TestArbiterAllMembersFallback(t *testing.T) {
	a := NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 10})
	ctx := context.Background()

	members := []GroupMember{
		{AgentID: "agent-a"},
		{AgentID: "agent-b"},
		{AgentID: "agent-c"},
	}

	// No @mention, no channelAgentID, AllMembersFallback=true → all members.
	d := a.Decide(ctx, "g1", nil, members, "", DecideOptions{AllMembersFallback: true})
	if len(d.RespondingAgents) != 3 {
		t.Fatalf("expected 3 agents, got %v", d.RespondingAgents)
	}

	// No @mention, no channelAgentID, AllMembersFallback=false → empty (CR-012).
	d2 := a.Decide(ctx, "g1", nil, members, "")
	if len(d2.RespondingAgents) != 0 {
		t.Fatalf("expected empty (CR-012), got %v", d2.RespondingAgents)
	}

	// @mention with AllMembersFallback=true → only mentioned agent.
	mentions := []pkgchannel.Mention{{PlatformID: "bot1", AgentID: "agent-b"}}
	d3 := a.Decide(ctx, "g1", mentions, members, "", DecideOptions{AllMembersFallback: true})
	if len(d3.RespondingAgents) != 1 || d3.RespondingAgents[0] != "agent-b" {
		t.Fatalf("expected [agent-b], got %v", d3.RespondingAgents)
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
