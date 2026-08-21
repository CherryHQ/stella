package lcm

import (
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Group turns share the canonical durable codec with every other session. The
// legacy inline path survives for exactly one case -- raw provider media, which
// a group cannot canonicalize while ctx_conversation_group_owner_check pins a
// group conversation's owner to the group UUID and ctx_media is owned by a user.
func TestGroupDurableWriteUsesCanonicalCodecExceptRawMedia(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendWindowMessage(t, store, "codec-group", eventlog.ActorHuman, "u1", "Ann", "hello")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	session := groupSess("agent-a", first.GroupID)
	ctx := groupCtx(first.Seq)
	q := sqlc.New(db)

	// The legacy codec stored any block list as a multimodal JSON blob, so its
	// text was unreadable to search and mis-costed for tokens. Canonical stores
	// the flattened projection and keeps the row plain text.
	blocks := ai.UserMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: "[seq:1 Ann]:"}, ai.TextContent{Text: "deploy now"},
	}}
	if err := p.Append(ctx, session, blocks); err != nil {
		t.Fatalf("canonical group user blocks: %v", err)
	}
	// A text-only tool result was stored with the whole envelope as its token
	// text under the legacy codec; canonical costs only the projection.
	result := ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "shell", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}}
	if err := p.Append(ctx, session, result); err != nil {
		t.Fatalf("canonical group tool result: %v", err)
	}
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored rows = %d, want 2", len(rows))
	}
	user, tool := rows[0], rows[1]
	if user.EventType != "text" || strings.Contains(user.Content, `"kind"`) {
		t.Fatalf("group user blocks kept the legacy JSON codec: type=%s content=%q", user.EventType, user.Content)
	}
	if !strings.Contains(user.Content, "deploy now") {
		t.Fatalf("canonical projection lost its text: %q", user.Content)
	}
	if want := int64(memory.EstimateTokens("ok")); tool.TokenCount != want {
		t.Fatalf("tool result token count = %d, want %d (envelope JSON was counted)", tool.TokenCount, want)
	}

	// Raw provider bytes still commit: a group runner has no canonicalizer, so
	// refusing them here would fail the whole turn instead of one block.
	raw := ai.ToolResultMessage{
		ToolCallID: "call-2", ToolName: "screenshot",
		Content: []ai.ContentBlock{ai.ImageContent{MimeType: "image/png", Data: "aGk="}},
	}
	if err := p.Append(ctx, session, raw); err != nil {
		t.Fatalf("raw group media must fall back to the legacy codec: %v", err)
	}

	// The fallback is scoped to raw media, not to every canonical failure: a
	// malformed reference has a canonical spelling and must stay an error.
	broken := ai.UserMessage{Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: " "}}}
	if err := p.Append(ctx, session, broken); err == nil {
		t.Fatal("malformed image ref silently fell back to the legacy codec")
	}

	// A direct session rejects raw media outright; only groups get the fallback.
	dm := memory.Session{ID: "agent-a:dm", AgentID: "agent-a", UserID: "user-1"}
	if err := p.Append(ctx, dm, raw); !errors.Is(err, ai.ErrRawImageContent) {
		t.Fatalf("direct session accepted raw media: %v", err)
	}
}
