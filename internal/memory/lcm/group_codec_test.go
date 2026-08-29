package lcm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Group turns share one durable codec with every other session, media
// included: ctx_media is owned by the group whose conversation references it,
// so a group image has a canonical spelling and raw provider bytes are a bug
// here exactly as they are in a direct session.
func TestGroupDurableWriteUsesCanonicalCodec(t *testing.T) {
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

	// A group-owned image commits as parts plus a media reference, the same
	// shape a direct session writes.
	mediaID := seedGroupMedia(t, db, first.GroupID)
	image := ai.ToolResultMessage{
		ToolCallID: "call-2", ToolName: "screenshot",
		Content: []ai.ContentBlock{ai.TextContent{Text: "here"}, ai.ImageRefContent{MediaID: mediaID}},
	}
	if err := p.Append(ctx, session, image); err != nil {
		t.Fatalf("canonical group media: %v", err)
	}
	rows, err = q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := q.GetMessageParts(ctx, rows[len(rows)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[1].PartType != "image" || parts[1].MediaID.String != mediaID {
		t.Fatalf("group media parts = %+v, want text + image reference", parts)
	}

	// Ownership is enforced at the write: a group cannot reference a user's
	// media, which is the same check that keeps one user out of another's.
	foreign := ai.UserMessage{Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: seedUserMedia(t, db)}}}
	if err := p.Append(ctx, session, foreign); !errors.Is(err, errCanonicalMediaUnavailable) {
		t.Fatalf("group referenced user-owned media: %v", err)
	}

	// A malformed reference is still an error, in a group as anywhere else.
	broken := ai.UserMessage{Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: " "}}}
	if err := p.Append(ctx, session, broken); err == nil {
		t.Fatal("malformed image ref committed")
	}

	// Raw provider bytes have no durable spelling in any session, group included.
	raw := ai.ToolResultMessage{
		ToolCallID: "call-3", ToolName: "screenshot",
		Content: []ai.ContentBlock{ai.ImageContent{MimeType: "image/png", Data: "aGk="}},
	}
	if err := p.Append(ctx, session, raw); !errors.Is(err, ai.ErrRawImageContent) {
		t.Fatalf("group session accepted raw media: %v", err)
	}
	dm := memory.Session{ID: "agent-a:dm", AgentID: "agent-a", UserID: "user-1"}
	if err := p.Append(ctx, dm, raw); !errors.Is(err, ai.ErrRawImageContent) {
		t.Fatalf("direct session accepted raw media: %v", err)
	}
}

func seedGroupMedia(t *testing.T, db *pgxpool.Pool, groupID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO ctx_media (group_id, sha256, mime_type, size_bytes)
		VALUES ($1, $2, 'image/png', 3) RETURNING id`,
		groupID, bytes.Repeat([]byte{7}, 32)).Scan(&id); err != nil {
		t.Fatalf("seed group media: %v", err)
	}
	return id
}

func seedUserMedia(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	userID := uuid.NewString()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@test.local"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var id string
	if err := db.QueryRow(ctx, `
		INSERT INTO ctx_media (user_id, sha256, mime_type, size_bytes)
		VALUES ($1, $2, 'image/png', 3) RETURNING id`,
		userID, bytes.Repeat([]byte{9}, 32)).Scan(&id); err != nil {
		t.Fatalf("seed user media: %v", err)
	}
	return id
}
