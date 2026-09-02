package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A group session reads its images through the same endpoint a direct session
// uses: the owner is derived from the session, so the web media route resolves
// group-owned bytes without a group-specific branch -- and a user principal
// still cannot reach them.
func TestReadMediaResolvesGroupOwnedImages(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	q := sqlc.New(pool)
	store := cfgstore.NewDBStore(pool)
	const agentID = "media-owner-agent"
	if err := store.CreateAgent(ctx, config.Agent{ID: agentID, Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	groupID := uuid.New()
	if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID: groupID.String(), Platform: "test", PlatformGroupID: "group-1", GroupName: "media group",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mem := memorytest.New()
	const sessionID = "media-owner-group-session"
	// A group session's principal is the group itself, in the session record and
	// in the conversation row the media lookup joins through.
	if err := mem.SaveInfo(ctx, memory.SessionInfo{
		ID: sessionID, UserID: groupID.String(), GroupID: groupID.String(), AgentID: agentID,
		Channel: "web", Kind: "chat", CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	conversation, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, Channel: "web", Kind: "chat", LastActive: now,
		AgentID: pgtype.Text{String: agentID, Valid: true},
		UserID:  pgtype.Text{String: groupID.String(), Valid: true},
		GroupID: pgtype.Text{String: groupID.String(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("group png bytes")
	digest := sha256.Sum256(data)
	home := t.TempDir()
	assets, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := assets.SessionMedia().PutSessionMedia(ctx, asset.GroupMediaOwner(groupID), digest, data); err != nil {
		t.Fatal(err)
	}
	media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		GroupID: pgtype.Text{String: groupID.String(), Valid: true},
		Sha256:  digest[:], MimeType: "image/png", SizeBytes: int64(len(data)),
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ID: uuid.NewString(), ConversationID: conversation.ID, Seq: 1, Role: "user",
		EventType: "text", Content: "look", TokenCount: 1, ActorType: string(eventlog.ActorHuman),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID: uuid.NewString(), MessageID: message.ID, PartType: "image", Ordinal: 0,
		MediaID: pgtype.Text{String: media.ID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(mem, pool, store, assets, agentaccess.NewService(store, appdb.NewAuthStore(pool)))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewGroupAgentAuthority(authz.GroupID(groupID.String()), authz.AgentID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	access, err := svc.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	got, err := access.ReadMedia(ctx, agentID, sessionID, media.ID)
	if err != nil {
		t.Fatalf("read group media: %v", err)
	}
	if !bytes.Equal(got.Data, data) || got.MimeType != "image/png" {
		t.Fatalf("media = %q (%s), want the stored bytes", got.Data, got.MimeType)
	}

	// The same blob is unreachable from a direct session: its owner is the
	// group, and ownership, not the session id alone, gates the read.
	userID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID.String(), "media-owner@test.local"); err != nil {
		t.Fatal(err)
	}
	const directID = "media-owner-direct-session"
	if err := mem.SaveInfo(ctx, memory.SessionInfo{
		ID: directID, UserID: userID.String(), AgentID: agentID,
		Channel: "web", Kind: "chat", CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: directID, Channel: "web", Kind: "chat", LastActive: now,
		AgentID: pgtype.Text{String: agentID, Valid: true},
		UserID:  pgtype.Text{String: userID.String(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	userAuthority, err := authz.NewUserAuthority(authz.UserID(userID.String()), false)
	if err != nil {
		t.Fatal(err)
	}
	userAccess, err := svc.Begin(ctx, userAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userAccess.ReadMedia(ctx, agentID, directID, media.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("direct session read group media: %v", err)
	}
}
