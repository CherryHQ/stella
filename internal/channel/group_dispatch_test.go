package channel

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/vision"
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
	reg.Unregister("telegram", "my_bot", "different-channel")
	if _, ok := reg.ChannelIDForBot("telegram", "my_bot"); !ok {
		t.Fatal("mismatched unregister removed bot identity")
	}
	reg.Unregister("telegram", "my_bot", "ch-1")
	if _, ok := reg.ChannelIDForBot("telegram", "my_bot"); ok {
		t.Fatal("expected bot identity to be removed")
	}
}

// A Feishu open_id is scoped to the receiving app, so a peer bot's id never
// matches what that peer registered for itself. The display name is the only
// identity shared across apps -- and an ambiguous one is worse than none.
func TestBotIdentityRegistryNameFallback(t *testing.T) {
	reg := NewBotIdentityRegistry()
	reg.RegisterName("feishu", "StellaDev", "ch-dev")

	if id, ok := reg.ChannelIDForBotName("feishu", "stelladev"); !ok || id != "ch-dev" {
		t.Fatalf("case-insensitive lookup = %q (ok=%v), want ch-dev", id, ok)
	}
	if _, ok := reg.ChannelIDForBotName("telegram", "StellaDev"); ok {
		t.Fatal("name lookup crossed platforms")
	}

	reg.RegisterName("feishu", "StellaDev", "ch-other")
	if _, ok := reg.ChannelIDForBotName("feishu", "StellaDev"); ok {
		t.Fatal("an ambiguous name must not resolve")
	}

	reg.UnregisterName("feishu", "Coder", "ch-coder")
	reg.RegisterName("feishu", "Coder", "ch-coder")
	reg.UnregisterName("feishu", "Coder", "someone-else")
	if _, ok := reg.ChannelIDForBotName("feishu", "Coder"); !ok {
		t.Fatal("mismatched unregister removed the name")
	}
	reg.UnregisterName("feishu", "Coder", "ch-coder")
	if _, ok := reg.ChannelIDForBotName("feishu", "Coder"); ok {
		t.Fatal("expected the name to be removed")
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
		// Image-only messages must project to a non-empty placeholder so the
		// group triage does not treat them as "nothing to route" (Major 1).
		{"image only", []ai.ContentBlock{
			ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
		}, imageContentPlaceholder},
		{"multiple images only", []ai.ContentBlock{
			ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
			ai.ImageContent{Data: "Ynll", MimeType: "image/jpeg"},
		}, imageContentPlaceholder},
		// Real text wins over the placeholder when both are present.
		{"text and image", []ai.ContentBlock{
			ai.TextContent{Text: "look"},
			ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
		}, "look"},
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

func TestLegacyGroupContentEnforcesInlineLimitAtStorageBoundary(t *testing.T) {
	oversized := base64.StdEncoding.EncodeToString(make([]byte, vision.MaxRendererPayloadBytes+1))
	blocks := legacyGroupContent([]ai.ContentBlock{
		ai.TextContent{Text: "saved path remains visible"},
		ai.ImageContent{Data: oversized, MimeType: "image/png"},
	})
	if len(blocks) != 2 || ai.HasImage(blocks) {
		t.Fatalf("legacy group blocks = %#v, want text-only degradation", blocks)
	}
	if got := ai.FlattenText(blocks); !strings.Contains(got, ai.UnavailableImageProjection) {
		t.Fatalf("legacy group projection = %q, want unavailable marker", got)
	}
}

func TestMarshalGroupContentBlocks(t *testing.T) {
	textOnly := []ai.ContentBlock{ai.TextContent{Text: "hello"}}
	if got := marshalGroupContentBlocks(textOnly); got != nil {
		t.Errorf("text-only blocks = %s, want nil (replay from text projection)", got)
	}

	withImage := []ai.ContentBlock{
		ai.TextContent{Text: "look"},
		ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
	}
	data := marshalGroupContentBlocks(withImage)
	if data == nil {
		t.Fatal("image-bearing blocks must serialize")
	}
	blocks, err := ai.UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 2 || !ai.HasImage(blocks) {
		t.Fatalf("round-trip = %#v, want text+image", blocks)
	}
}

func TestGroupMessageContentRehydration(t *testing.T) {
	// Legacy/text-only row: empty JSON array falls back to the text projection.
	legacy := sqlc.CtxGroupMessage{Content: "just text", ContentBlocks: []byte("[]")}
	blocks := groupMessageContentBlocks(legacy)
	if len(blocks) != 1 {
		t.Fatalf("legacy blocks = %#v, want single text block", blocks)
	}
	if tc, ok := blocks[0].(ai.TextContent); !ok || tc.Text != "just text" {
		t.Fatalf("legacy block = %#v, want text projection", blocks[0])
	}

	// Image-bearing row: structured blocks win over the text projection.
	withImage := sqlc.CtxGroupMessage{
		Content:       "look",
		ContentBlocks: []byte(`[{"kind":"text","text":"look"},{"kind":"image","data":"aGk=","mime_type":"image/png"}]`),
	}
	blocks = groupMessageContentBlocks(withImage)
	if len(blocks) != 2 || !ai.HasImage(blocks) {
		t.Fatalf("rehydrated blocks = %#v, want text+image", blocks)
	}

	// Corrupt blocks degrade to the text projection instead of dropping the message.
	corrupt := sqlc.CtxGroupMessage{Content: "still here", ContentBlocks: []byte("{not json")}
	blocks = groupMessageContentBlocks(corrupt)
	if len(blocks) != 1 || ai.HasImage(blocks) {
		t.Fatalf("corrupt blocks = %#v, want text fallback", blocks)
	}
}

// TestImageOnlyMessageProjectionAndRehydration pins the Major 1 contract: an
// image-only group message stores a non-empty "[image]" text projection (so the
// group triage routes it instead of dropping it), yet dispatch rehydrates
// the real image blocks — the placeholder must never leak into them.
func TestImageOnlyMessageProjectionAndRehydration(t *testing.T) {
	blocks := []ai.ContentBlock{ai.ImageContent{Data: "aGk=", MimeType: "image/png"}}

	// Ingest-side projection: what the group triage and history assembly see.
	content := contentBlocksToText(blocks)
	if content != imageContentPlaceholder {
		t.Fatalf("image-only projection = %q, want %q", content, imageContentPlaceholder)
	}
	if content == "" {
		t.Fatal("triage-visible projection must be non-empty for image-only messages")
	}

	// Persisted row: placeholder in content, real blocks in content_blocks.
	stored := marshalGroupContentBlocks(blocks)
	if stored == nil {
		t.Fatal("image-bearing blocks must serialize to content_blocks")
	}
	msg := sqlc.CtxGroupMessage{Content: content, ContentBlocks: stored}

	// Dispatch-side rehydration must prefer content_blocks and return the real
	// image, with no "[image]" placeholder text block leaking through.
	got := groupMessageContentBlocks(msg)
	if len(got) != 1 || !ai.HasImage(got) {
		t.Fatalf("rehydrated blocks = %#v, want a single image block", got)
	}
	for _, b := range got {
		if tc, ok := b.(ai.TextContent); ok && tc.Text == imageContentPlaceholder {
			t.Fatalf("placeholder %q leaked into rehydrated blocks", imageContentPlaceholder)
		}
	}
}

func openGroupTestDB(t *testing.T) *eventlog.Store {
	t.Helper()
	db := dbtest.New(t)
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

func TestImportGroupHistoryPersistsCanonicalRowsWithoutOutbox(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	agentID := ts.stellaAgentID(t)
	if _, err := ts.db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('discord-history', 'Discord history', 'discord', $1, true)`, agentID); err != nil {
		t.Fatal(err)
	}
	coord := &Coordinator{eventLog: eventlog.NewStore(ts.db), store: ts.store}
	history := []pkgchannel.IncomingMessage{
		{Platform: "discord", ChannelID: "discord-history", ChatID: "forum", ThreadID: "thread", IsGroup: true, SenderID: "alice", MessageID: "starter", Content: pkgchannel.TextContent("question")},
		{Platform: "discord", ChannelID: "discord-history", ChatID: "forum", ThreadID: "thread", IsGroup: true, SenderID: "bob", MessageID: "reply", Content: pkgchannel.TextContent("detail")},
	}
	if err := coord.ImportGroupHistory(ctx, history); err != nil {
		t.Fatal(err)
	}
	if err := coord.ImportGroupHistory(ctx, history); err != nil {
		t.Fatal(err)
	}
	var messages, outbox int
	if err := ts.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := ts.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if messages != 2 || outbox != 0 {
		t.Fatalf("imported messages=%d outbox=%d, want 2 and 0", messages, outbox)
	}
}

func TestResolveMentionAgents(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	q := sqlc.New(ts.db)

	agentID := ts.stellaAgentID(t)

	// Create a channel for the bot.
	if _, err := ts.db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('ch-bot1', 'Bot1', 'telegram', $1, true)`, agentID); err != nil {
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

	// Helper to fetch members from DB.
	fetchMembers := func(t *testing.T, gid string) []GroupMember {
		t.Helper()
		rows, err := q.ListGroupMembers(ctx, gid)
		if err != nil {
			t.Fatalf("list group members: %v", err)
		}
		members := make([]GroupMember, len(rows))
		for i, r := range rows {
			members[i] = GroupMember{AgentID: r.AgentID, ReplyChannelID: r.ReplyChannelID}
		}
		return members
	}

	// Set up the coordinator with enough deps for mention resolution.
	coord := &Coordinator{
		store:       ts.store,
		botRegistry: reg,
	}

	// Resolution and the membership filter are separate steps now (resolution
	// runs before the group is known so the stored text can name agents), but
	// together they must still answer exactly what the group sees.
	resolve := func(t *testing.T, mentions []pkgchannel.Mention) {
		t.Helper()
		coord.resolveMentionAgents(ctx, "telegram", mentions)
		coord.clearNonMemberMentions("telegram", mentions, fetchMembers(t, groupID))
	}

	t.Run("known bot resolves to agent", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@bot1_username", PlatformID: "bot1_username"},
		}
		resolve(t, mentions)
		if mentions[0].AgentID != agentID {
			t.Errorf("expected AgentID=%q, got %q", agentID, mentions[0].AgentID)
		}
	})

	t.Run("unknown bot not resolved", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@unknown_bot", PlatformID: "unknown_bot"},
		}
		resolve(t, mentions)
		if mentions[0].AgentID != "" {
			t.Errorf("expected empty AgentID for unknown bot, got %q", mentions[0].AgentID)
		}
	})

	t.Run("known bot not in group not resolved", func(t *testing.T) {
		// Register another bot that is NOT a group member. Its distinct agent is
		// required by the one-bidirectional-channel-per-(agent,type) invariant.
		if _, err := ts.db.Exec(ctx, `INSERT INTO agent (id, name, model, enabled, workspace) VALUES ('agent-non-member', 'NonMember', 'test', true, 'default')`); err != nil {
			t.Fatalf("create agent: %v", err)
		}
		if _, err := ts.db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('ch-bot2', 'Bot2', 'telegram', 'agent-non-member', true)`); err != nil {
			t.Fatalf("create channel: %v", err)
		}
		reg.Register("telegram", "bot2_username", "ch-bot2")

		mentions := []pkgchannel.Mention{
			{Raw: "@bot2_username", PlatformID: "bot2_username"},
		}
		resolve(t, mentions)
		if mentions[0].AgentID != "" {
			t.Errorf("expected empty AgentID for non-member bot, got %q", mentions[0].AgentID)
		}
	})

	// An agent knows its peers by their Stella names, which is what the group
	// prompt lists. A platform display name is a different namespace, so an
	// addressed agent cannot tell it was the one addressed.
	t.Run("resolved mention is rewritten to the Stella agent name", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "bot1_username", PlatformID: "bot1_username"},
		}
		resolve(t, mentions)
		got := coord.rewriteMentionsToAgentNames(ctx, mentions, []ai.ContentBlock{
			ai.TextContent{Text: "@bot1_username 你来"},
		})
		text := contentBlocksToText(got)
		if want := "@Stella 你来"; text != want {
			t.Errorf("rewritten text = %q, want %q", text, want)
		}
	})

	t.Run("unresolved mention keeps the platform name", func(t *testing.T) {
		mentions := []pkgchannel.Mention{{Raw: "stranger", PlatformID: "stranger"}}
		resolve(t, mentions)
		got := coord.rewriteMentionsToAgentNames(ctx, mentions, []ai.ContentBlock{
			ai.TextContent{Text: "@stranger 你来"},
		})
		if text := contentBlocksToText(got); text != "@stranger 你来" {
			t.Errorf("rewritten text = %q, want it unchanged", text)
		}
	})

	t.Run("already resolved mention unchanged", func(t *testing.T) {
		mentions := []pkgchannel.Mention{
			{Raw: "@bot1_username", PlatformID: "bot1_username", AgentID: "pre-set"},
		}
		coord.resolveMentionAgents(ctx, "telegram", mentions)
		if mentions[0].AgentID != "pre-set" {
			t.Errorf("expected pre-set AgentID to be preserved, got %q", mentions[0].AgentID)
		}
	})
}

// TestGroupIncomingNewIsRefusedBeforeEventLog proves a platform group `/new` is
// refused explicitly — a group's context is shared, so no member's chat command
// may clear it — and that the refusal is answered before the event-log append,
// so the command never becomes part of the group's context either.
func TestGroupIncomingNewIsRefusedBeforeEventLog(t *testing.T) {
	db := dbtest.New(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	coord := &Coordinator{
		eventLog:      el,
		groupResolver: el,
	}
	msg := pkgchannel.IncomingMessage{
		Platform: "telegram", ChatID: "chat-new", SenderID: "alice",
		MessageID: "m-new", IsGroup: true,
		Content: []ai.ContentBlock{ai.TextContent{Text: "/new"}},
	}

	reply, handled, stream, err := coord.handleGroupIncoming(ctx, msg, "/new", "")
	if err != nil {
		t.Fatalf("handleGroupIncoming: %v", err)
	}
	if !handled || stream != nil {
		t.Fatalf("handled=%v stream=%v, want an immediate plain reply", handled, stream)
	}
	if reply != pkgchannel.GroupNewSessionUnsupportedMessage {
		t.Fatalf("reply = %q, want %q", reply, pkgchannel.GroupNewSessionUnsupportedMessage)
	}

	groupID, err := el.ResolveGroupID(ctx, "telegram", "chat-new", "")
	if err != nil {
		t.Fatalf("ResolveGroupID: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id = $1`, groupID).Scan(&count); err != nil {
		t.Fatalf("count group messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("/new appended %d group messages, want 0", count)
	}
}
