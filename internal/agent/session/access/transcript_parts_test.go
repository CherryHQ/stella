package access

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type countingDB struct {
	db          sqlc.DBTX
	partQueries int
}

func (d *countingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.db.Exec(ctx, sql, args...)
}

func (d *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "ctx_message_part") {
		d.partQueries++
	}
	return d.db.Query(ctx, sql, args...)
}

func (d *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.db.QueryRow(ctx, sql, args...)
}

func TestListMessagesLoadsPartsInTwoBatchesAndFallsBackToBaseline(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID.String(), "parts@test.local"); err != nil {
		t.Fatal(err)
	}
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "parts-agent", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	const sessionID = "parts-session"
	now := time.Now().UTC()
	mem := memorytest.New()
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: sessionID, UserID: userID.String(), AgentID: "parts-agent", Channel: "web", Kind: "chat", CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	conversation, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, UserID: pgtype.Text{String: userID.String(), Valid: true},
		AgentID: pgtype.Text{String: "parts-agent", Valid: true}, Channel: "web", Kind: "chat", LastActive: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("image bytes"))
	media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{UserID: userID.String(), Sha256: digest[:], MimeType: "image/png", SizeBytes: 11, WidthPx: 1, HeightPx: 1})
	if err != nil {
		t.Fatal(err)
	}
	userMessage, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{ID: uuid.NewString(), ConversationID: conversation.ID, Seq: 1, Role: "user", EventType: "multimodal", Content: "baseline", TokenCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.CreateMessage(ctx, sqlc.CreateMessageParams{ID: uuid.NewString(), ConversationID: conversation.ID, Seq: 2, Role: "user", EventType: "multimodal", Content: `[{"kind":"text","text":"legacy"}]`, TokenCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []sqlc.CreateMessagePartParams{
		{ID: uuid.NewString(), MessageID: userMessage.ID, PartType: "text", Ordinal: 0, TextContent: pgtype.Text{String: "before", Valid: true}},
		{ID: uuid.NewString(), MessageID: userMessage.ID, PartType: "image", Ordinal: 1, MediaID: pgtype.Text{String: media.ID, Valid: true}, TextContent: pgtype.Text{String: "stored baseline", Valid: true}, BaselineStatus: "ready", BaselineRenderer: "test", BaselineContract: 1},
		{ID: uuid.NewString(), MessageID: userMessage.ID, PartType: "text", Ordinal: 2, TextContent: pgtype.Text{String: "after", Valid: true}},
	} {
		if _, err := q.CreateMessagePart(ctx, part); err != nil {
			t.Fatal(err)
		}
	}
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingDB{db: db}
	svc, err := NewService(mem, counting, store, assets, agentaccess.NewService(store, appdb.NewAuthStore(db)))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := (auth.Subject{UserID: userID.String(), Roles: []string{auth.RoleUser}}).Authority()
	if err != nil {
		t.Fatal(err)
	}
	access, err := svc.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := access.ListMessages(ctx, MessageListInput{AgentID: "parts-agent", SessionID: sessionID, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if counting.partQueries != 2 {
		t.Fatalf("part queries = %d, want exactly 2 fixed batch queries", counting.partQueries)
	}
	if len(messages) != 2 || len(messages[0].Parts) != 3 {
		t.Fatalf("messages = %#v, want ordered canonical parts", messages)
	}
	if got := messages[0].Parts; got[0].Text != "before" || got[1].Type != "image" || got[1].MediaID != media.ID || got[2].Text != "after" {
		t.Fatalf("parts = %#v, want text/image/text order", got)
	}
	if len(messages[1].Parts) != 0 || messages[1].Content != `[{"kind":"text","text":"legacy"}]` {
		t.Fatalf("legacy message changed: %#v", messages[1])
	}
	if _, err := db.Exec(ctx, `DELETE FROM ctx_media WHERE id = $1`, media.ID); err != nil {
		t.Fatal(err)
	}
	messages, err = access.ListMessages(ctx, MessageListInput{AgentID: "parts-agent", SessionID: sessionID, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages[0].Parts[1]; got.Type != "text" || got.Text != "stored baseline" {
		t.Fatalf("deleted media part = %#v, want baseline text", got)
	}
}
