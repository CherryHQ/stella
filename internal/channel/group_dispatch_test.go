package channel

import (
	"context"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestBotIdentityRegistry(t *testing.T) {
	reg := NewBotIdentityRegistry()

	reg.Register("telegram", "my_bot", "ch-1")
	reg.Register("telegram", "other_bot", "ch-2")
	reg.Register("feishu", "my_bot", "ch-3") // same name, different platform

	if id, ok := reg.ChannelIDForBot("telegram", "my_bot"); !ok || id != "ch-1" {
		t.Fatalf("expected ch-1, got %q (ok=%v)", id, ok)
	}
	if id, ok := reg.ChannelIDForBot("telegram", "other_bot"); !ok || id != "ch-2" {
		t.Fatalf("expected ch-2, got %q (ok=%v)", id, ok)
	}
	if id, ok := reg.ChannelIDForBot("feishu", "my_bot"); !ok || id != "ch-3" {
		t.Fatalf("expected ch-3, got %q (ok=%v)", id, ok)
	}
	if _, ok := reg.ChannelIDForBot("telegram", "unknown"); ok {
		t.Fatal("expected unknown bot to not be found")
	}
}

func TestContentBlocksToText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ai.ContentBlock
		want   string
	}{
		{"nil", nil, ""},
		{"empty", []ai.ContentBlock{}, ""},
		{"single text", []ai.ContentBlock{ai.TextContent{Text: "hello"}}, "hello"},
		{"multiple text", []ai.ContentBlock{
			ai.TextContent{Text: "hello"},
			ai.TextContent{Text: "world"},
		}, "hello\nworld"},
		{"mixed with empty", []ai.ContentBlock{
			ai.TextContent{Text: "hello"},
			ai.TextContent{Text: ""},
			ai.TextContent{Text: "world"},
		}, "hello\nworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contentBlocksToText(tt.blocks)
			if got != tt.want {
				t.Errorf("contentBlocksToText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstMentionedAgent(t *testing.T) {
	if got := firstMentionedAgent(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := firstMentionedAgent([]pkgchannel.Mention{
		{PlatformID: "bot1"},
		{PlatformID: "bot2"},
	}); got != "" {
		t.Errorf("expected empty (no AgentID), got %q", got)
	}
	if got := firstMentionedAgent([]pkgchannel.Mention{
		{PlatformID: "bot1"},
		{PlatformID: "bot2", AgentID: "agent-2"},
		{PlatformID: "bot3", AgentID: "agent-3"},
	}); got != "agent-2" {
		t.Errorf("expected agent-2, got %q", got)
	}
}

func openGroupTestDB(t *testing.T) *eventlog.Store {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return eventlog.NewStore(db)
}

func TestGroupMessageAppendDedup(t *testing.T) {
	store := openGroupTestDB(t)
	ctx := context.Background()

	msg := eventlog.Message{
		Platform:          "telegram",
		PlatformGroupID:   "chat123",
		ActorType:         eventlog.ActorHuman,
		ActorID:           "alice",
		PlatformMessageID: "msg-42",
		Content:           "hello group",
	}

	r1, err := store.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !r1.Inserted {
		t.Fatal("first append should insert")
	}

	r2, err := store.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if r2.Inserted {
		t.Fatal("second append should be deduplicated")
	}
	if r2.GroupID != r1.GroupID || r2.Seq != r1.Seq {
		t.Fatalf("dedup should return same group/seq: got %s/%d vs %s/%d", r2.GroupID, r2.Seq, r1.GroupID, r1.Seq)
	}
}

func TestResolveMentionAgents(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	q := sqlc.New(ts.db)

	agentID := ts.stellaAgentID(t)

	// Create a channel for the bot.
	if _, err := ts.db.ExecContext(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('ch-bot1', 'Bot1', 'telegram', ?, 1)`, agentID); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Create a group.
	elStore := eventlog.NewStore(ts.db)
	groupID, err := elStore.ResolveGroupID(ctx, "telegram", "group1", "")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	// Add the agent to the group.
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        groupID,
		AgentID:        agentID,
		ReplyChannelID: "ch-bot1",
	}); err != nil {
		t.Fatalf("add group member: %v", err)
	}

	// Set up the bot registry.
	reg := NewBotIdentityRegistry()
	reg.Register("telegram", "bot1_username", "ch-bot1")

	// Set up the coordinator with enough deps for mention resolution.
	coord := &Coordinator{
		store:       ts.store,
		botRegistry: reg,
		memberLister: FuncGroupMemberLister(func(ctx context.Context, gid string) ([]GroupMember, error) {
			rows, err := q.ListGroupMembers(ctx, gid)
			if err != nil {
				return nil, err
			}
			members := make([]GroupMember, len(rows))
			for i, r := range rows {
				members[i] = GroupMember{AgentID: r.AgentID, ReplyChannelID: r.ReplyChannelID}
			}
			return members, nil
		}),
	}

	t.Run("known bot resolves to agent", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@bot1_username", PlatformID: "bot1_username"},
		}
		coord.resolveMentionAgents(ctx, groupID, "telegram", mentions)
		if mentions[0].AgentID != agentID {
			t.Errorf("expected AgentID=%q, got %q", agentID, mentions[0].AgentID)
		}
	})

	t.Run("unknown bot not resolved", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@unknown_bot", PlatformID: "unknown_bot"},
		}
		coord.resolveMentionAgents(ctx, groupID, "telegram", mentions)
		if mentions[0].AgentID != "" {
			t.Errorf("expected empty AgentID for unknown bot, got %q", mentions[0].AgentID)
		}
	})

	t.Run("known bot not in group not resolved", func(t *testing.T) {
		// Register another bot that is NOT a group member.
		if _, err := ts.db.ExecContext(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('ch-bot2', 'Bot2', 'telegram', ?, 1)`, agentID); err != nil {
			t.Fatalf("create channel: %v", err)
		}
		// Create a second agent for the non-member channel.
		if _, err := ts.db.ExecContext(ctx, `INSERT INTO agent (id, name, model, enabled, workspace) VALUES ('agent-non-member', 'NonMember', 'test', 1, 'default')`); err != nil {
			t.Fatalf("create agent: %v", err)
		}
		if _, err := ts.db.ExecContext(ctx, `UPDATE channel SET agent_id = 'agent-non-member' WHERE id = 'ch-bot2'`); err != nil {
			t.Fatalf("update channel: %v", err)
		}
		reg.Register("telegram", "bot2_username", "ch-bot2")

		mentions := []pkgchannel.Mention{
			{Raw: "@bot2_username", PlatformID: "bot2_username"},
		}
		coord.resolveMentionAgents(ctx, groupID, "telegram", mentions)
		if mentions[0].AgentID != "" {
			t.Errorf("expected empty AgentID for non-member bot, got %q", mentions[0].AgentID)
		}
	})

	t.Run("already resolved mention unchanged", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@bot1_username", PlatformID: "bot1_username", AgentID: "pre-set"},
		}
		coord.resolveMentionAgents(ctx, groupID, "telegram", mentions)
		if mentions[0].AgentID != "pre-set" {
			t.Errorf("expected pre-set AgentID to be preserved, got %q", mentions[0].AgentID)
		}
	})
}

func TestFuncGroupMemberLister(t *testing.T) {
	lister := FuncGroupMemberLister(func(_ context.Context, groupID string) ([]GroupMember, error) {
		if groupID == "g1" {
			return []GroupMember{
				{AgentID: "a1", ReplyChannelID: "ch1"},
				{AgentID: "a2", ReplyChannelID: "ch2"},
			}, nil
		}
		return nil, nil
	})

	members, err := lister.ListGroupMembers(context.Background(), "g1")
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if members[0].AgentID != "a1" {
		t.Errorf("expected a1, got %s", members[0].AgentID)
	}
}
