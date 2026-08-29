package channel

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
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

// The stored text projection is what group triage reads, and triage treats an
// empty message as "nothing to route". A canonical reference must therefore
// always project to something, with or without a rendered baseline.
func TestGroupTextProjection(t *testing.T) {
	described := ai.ImageRefContent{MediaID: "media-1", Baseline: ai.ImageBaseline{Text: "## Text\nsign\n\n## Scene\na street sign"}}
	bare := ai.ImageRefContent{MediaID: "media-2"}
	tests := []struct {
		name   string
		blocks []ai.ContentBlock
		want   string
	}{
		{"nil", nil, ""},
		{"single text", []ai.ContentBlock{ai.TextContent{Text: "hello"}}, "hello"},
		{"multiple text", []ai.ContentBlock{ai.TextContent{Text: "hello"}, ai.TextContent{Text: "world"}}, "hello world"},
		{"image only without baseline", []ai.ContentBlock{bare}, ai.UnavailableImageProjection},
		{"image only with baseline", []ai.ContentBlock{described}, described.Baseline.Text},
		{"text and image", []ai.ContentBlock{ai.TextContent{Text: "look"}, bare}, "look " + ai.UnavailableImageProjection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ai.FlattenCanonicalText(tt.blocks); got != tt.want {
				t.Errorf("projection = %q, want %q", got, tt.want)
			}
			if len(tt.blocks) > 0 && ai.HasImageRef(tt.blocks) && ai.FlattenCanonicalText(tt.blocks) == "" {
				t.Error("an image-bearing message must never project to empty text")
			}
		})
	}
}

// Media handling must never cost the group its message: an unusable image
// degrades to the stable unavailable marker, and the text survives.
func TestUnavailableImagesKeepsTheMessage(t *testing.T) {
	blocks := unavailableImages([]ai.ContentBlock{
		ai.TextContent{Text: "saved path remains visible"},
		ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
	})
	if len(blocks) != 2 || ai.HasImage(blocks) {
		t.Fatalf("degraded blocks = %#v, want text-only", blocks)
	}
	if got := ai.FlattenCanonicalText(blocks); !strings.Contains(got, ai.UnavailableImageProjection) {
		t.Fatalf("degraded projection = %q, want unavailable marker", got)
	}
}

func TestMarshalGroupContentBlocks(t *testing.T) {
	textOnly := []ai.ContentBlock{ai.TextContent{Text: "hello"}}
	if got := marshalGroupContentBlocks(textOnly); got != nil {
		t.Errorf("text-only blocks = %s, want nil (replay from text projection)", got)
	}

	withRef := []ai.ContentBlock{
		ai.TextContent{Text: "look"},
		ai.ImageRefContent{MediaID: "media-1"},
	}
	data := marshalGroupContentBlocks(withRef)
	if data == nil {
		t.Fatal("reference-bearing blocks must serialize")
	}
	if !strings.Contains(string(data), `"image_ref"`) {
		t.Fatalf("stored blocks = %s, want a canonical reference", data)
	}
	blocks, err := ai.UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 2 || !ai.HasImageRef(blocks) || ai.HasImage(blocks) {
		t.Fatalf("round-trip = %#v, want text + canonical reference", blocks)
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

	// Canonical row: structured blocks win over the text projection.
	withRef := sqlc.CtxGroupMessage{
		Content:       "look",
		ContentBlocks: []byte(`[{"kind":"text","text":"look"},{"kind":"image_ref","media_id":"media-1"}]`),
	}
	blocks = groupMessageContentBlocks(withRef)
	if len(blocks) != 2 || !ai.HasImageRef(blocks) {
		t.Fatalf("rehydrated blocks = %#v, want text + reference", blocks)
	}

	// Rows written before group canonical media still carry inline bytes and
	// must keep replaying, or their history would lose the image.
	inline := sqlc.CtxGroupMessage{
		Content:       "look",
		ContentBlocks: []byte(`[{"kind":"image","data":"aGk=","mime_type":"image/png"}]`),
	}
	if blocks = groupMessageContentBlocks(inline); len(blocks) != 1 || !ai.HasImage(blocks) {
		t.Fatalf("legacy inline row = %#v, want the raw image", blocks)
	}

	// Corrupt blocks degrade to the text projection instead of dropping the message.
	corrupt := sqlc.CtxGroupMessage{Content: "still here", ContentBlocks: []byte("{not json")}
	blocks = groupMessageContentBlocks(corrupt)
	if len(blocks) != 1 || ai.HasImage(blocks) {
		t.Fatalf("corrupt blocks = %#v, want text fallback", blocks)
	}
}

// An image-only group message stores a non-empty projection (so triage routes
// it) while dispatch rehydrates the canonical reference: the placeholder lives
// in the text column only and must never reach the model as a block.
func TestImageOnlyMessageProjectionAndRehydration(t *testing.T) {
	blocks := []ai.ContentBlock{ai.ImageRefContent{MediaID: "media-1"}}

	content := ai.FlattenCanonicalText(blocks)
	if content != ai.UnavailableImageProjection || content == "" {
		t.Fatalf("image-only projection = %q, want the stable placeholder", content)
	}

	stored := marshalGroupContentBlocks(blocks)
	if stored == nil {
		t.Fatal("reference-bearing blocks must serialize to content_blocks")
	}
	got := groupMessageContentBlocks(sqlc.CtxGroupMessage{Content: content, ContentBlocks: stored})
	if len(got) != 1 || !ai.HasImageRef(got) {
		t.Fatalf("rehydrated blocks = %#v, want a single reference", got)
	}
	for _, b := range got {
		if tc, ok := b.(ai.TextContent); ok && tc.Text == content {
			t.Fatalf("placeholder %q leaked into rehydrated blocks", content)
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
		text := ai.FlattenCanonicalText(got)
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
		if text := ai.FlattenCanonicalText(got); text != "@stranger 你来" {
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

// Text mentions are a fallback, not a second routing protocol: they resolve
// without a native payload and never duplicate an already resolved native one.
func TestPlatformTextMentionFallsBackWithoutDuplicatingNativeMention(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	q := sqlc.New(ts.db)
	agentID := ts.stellaAgentID(t)
	if _, err := ts.db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('ch-bot1', 'Bot1', 'telegram', $1, true)`, agentID); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	el := eventlog.NewStore(ts.db)
	groupID, err := el.ResolveGroupID(ctx, "telegram", "group-text-mention", "")
	if err != nil {
		t.Fatalf("ResolveGroupID: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{GroupID: groupID, AgentID: agentID, ReplyChannelID: "ch-bot1"}); err != nil {
		t.Fatalf("AddGroupMember: %v", err)
	}
	registry := NewBotIdentityRegistry()
	registry.Register("telegram", "bot1_username", "ch-bot1")
	coord := &Coordinator{eventLog: el, store: ts.store, botRegistry: registry}

	appendAndReadMentions := func(t *testing.T, id string, native []pkgchannel.Mention) []pkgchannel.Mention {
		t.Helper()
		result, err := coord.appendGroupMessage(ctx, pkgchannel.IncomingMessage{
			Platform: "telegram", ChannelID: "ch-bot1", ChatID: "group-text-mention", SenderID: "alice", MessageID: id,
			Content: pkgchannel.TextContent("please ask @Stella"), Mentions: native,
		})
		if err != nil {
			t.Fatalf("appendGroupMessage: %v", err)
		}
		outbox, err := q.GetGroupOutboxByMessage(ctx, result.Message.ID)
		if err != nil {
			t.Fatalf("GetGroupOutboxByMessage: %v", err)
		}
		envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
		if err != nil {
			t.Fatalf("DecodeGroupOutboxEnvelope: %v", err)
		}
		return envelope.Mentions
	}

	textOnly := appendAndReadMentions(t, "text-only", nil)
	if want := []pkgchannel.Mention{{Raw: "@Stella", AgentID: agentID}}; !reflect.DeepEqual(textOnly, want) {
		t.Fatalf("text-only mentions = %#v, want %#v", textOnly, want)
	}
	nativeAndText := appendAndReadMentions(t, "native-and-text", []pkgchannel.Mention{{Raw: "bot1_username", PlatformID: "bot1_username"}})
	if len(nativeAndText) != 1 || nativeAndText[0].AgentID != agentID {
		t.Fatalf("native-and-text mentions = %#v, want one resolved mention for %q", nativeAndText, agentID)
	}
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
