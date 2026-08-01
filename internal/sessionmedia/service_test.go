package sessionmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestPersistDeduplicatesPerUserAndSeparatesUsers(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	home := t.TempDir()
	assets, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatalf("new asset store: %v", err)
	}
	svc, err := New(assets.SessionMedia(), q)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	userA := seedUser(t, db)
	userB := seedUser(t, db)
	in := Input{
		UserID:   userA,
		Data:     []byte("same immutable image bytes"),
		MimeType: "image/png",
		WidthPX:  64,
		HeightPX: 32,
	}
	first, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("first Persist: %v", err)
	}
	second, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("second Persist: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same-user duplicate IDs = %q, %q; want one row", first.ID, second.ID)
	}
	mediaID, err := uuid.Parse(first.ID)
	if err != nil || mediaID.Version() != 7 {
		t.Fatalf("database-generated media ID = %q (version %d), parse error = %v; want UUIDv7", first.ID, mediaID.Version(), err)
	}

	in.UserID = userB
	otherUser, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("cross-user Persist: %v", err)
	}
	if otherUser.ID == first.ID || otherUser.UserID == first.UserID {
		t.Fatalf("cross-user media reused metadata: first=%+v other=%+v", first, otherUser)
	}

	digest := sha256.Sum256(in.Data)
	for _, userID := range []uuid.UUID{userA, userB} {
		path := filepath.Join(home, "users", userID.String(), "session-media", hex.EncodeToString(digest[:]))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("user %s session-media object missing at its isolated key: %v", userID, err)
		}
	}
	got, err := assets.SessionMedia().OpenSessionMedia(ctx, userB, digest, int64(len(in.Data)))
	if err != nil || string(got) != string(in.Data) {
		t.Fatalf("open cross-user object = %q, %v", got, err)
	}
	if _, err := assets.SessionMedia().OpenSessionMedia(ctx, userA, digest, int64(len(in.Data))); err != nil {
		t.Fatalf("open same-user object: %v", err)
	}

	in.UserID = userA
	in.MimeType = "image/jpeg"
	if _, err := svc.Persist(ctx, in); !errors.Is(err, ErrMetadataMismatch) {
		t.Fatalf("same digest with incompatible metadata error = %v, want ErrMetadataMismatch", err)
	}
}

func TestLoadIsUserScopedAndVerifiesImmutableBytes(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForPool(assets.SessionMedia(), db)
	if err != nil {
		t.Fatal(err)
	}
	owner := seedUser(t, db)
	other := seedUser(t, db)
	fixture := imageBlock(t, 1)
	data, err := base64.StdEncoding.DecodeString(fixture.Data)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := svc.Persist(ctx, Input{UserID: owner, Data: data, MimeType: "image/png", WidthPX: 8, HeightPX: 8})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load(ctx, owner, stored.ID)
	if err != nil || loaded.MimeType != "image/png" || loaded.Data == "" {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
	if _, err := svc.Load(ctx, other, stored.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign load = %v, want opaque ErrNotFound", err)
	}
	if _, err := svc.Load(ctx, owner, "not-a-media-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed load = %v, want opaque ErrNotFound", err)
	}
}

func TestLoadPreparesProviderPayloadWithoutMutatingStoredOriginal(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForPool(assets.SessionMedia(), db)
	if err != nil {
		t.Fatal(err)
	}

	data := noisyPNG(t, 1400, 1400)
	if len(data) <= vision.MaxRendererPayloadBytes {
		t.Fatalf("fixture = %d bytes, want more than provider payload ceiling", len(data))
	}
	owner := seedUser(t, db)
	stored, err := svc.Persist(ctx, Input{UserID: owner, Data: data, MimeType: "image/png", WidthPX: 1400, HeightPX: 1400})
	if err != nil {
		t.Fatal(err)
	}
	if stored.SizeBytes != int64(len(data)) {
		t.Fatalf("stored size = %d, want %d", stored.SizeBytes, len(data))
	}
	digest := sha256.Sum256(data)
	persisted, err := assets.SessionMedia().OpenSessionMedia(ctx, owner, digest, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persisted, data) {
		t.Fatal("immutable asset bytes changed before history/provider projection")
	}

	loaded, err := svc.Load(ctx, owner, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(loaded.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > vision.MaxRendererPayloadBytes {
		t.Fatalf("provider payload = %d bytes, exceeds %d", len(payload), vision.MaxRendererPayloadBytes)
	}
	persisted, err = assets.SessionMedia().OpenSessionMedia(ctx, owner, digest, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persisted, data) {
		t.Fatal("provider hydration mutated immutable asset/history bytes")
	}
}

func noisyPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(1)
	for y := range height {
		for x := range width {
			state = state*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{R: uint8(state), G: uint8(state >> 8), B: uint8(state >> 16), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func TestSessionScopedMediaLookupAndPartBatch(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("new asset store: %v", err)
	}
	svc, err := New(assets.SessionMedia(), q)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	user := seedUser(t, db)
	const agentID = "session-media-agent"
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Media Agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	media, err := svc.Persist(ctx, Input{
		UserID: user, Data: []byte("media bytes"), MimeType: "image/png", WidthPX: 8, HeightPX: 4,
	})
	if err != nil {
		t.Fatalf("persist media: %v", err)
	}
	conversationID := uuid.NewString()
	const sessionID = "session-media-test"
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id)
		VALUES ($1, $2, 'web', 'chat', $3, $4)`, conversationID, sessionID, agentID, user.String()); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	messageID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count)
		VALUES ($1, $2, 1, 'user', 'text', 'baseline text', 2)`, messageID, conversationID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID:               uuid.NewString(),
		MessageID:        messageID,
		PartType:         "image",
		Ordinal:          0,
		MediaID:          pgtype.Text{String: media.ID, Valid: true},
		BaselineStatus:   "ready",
		BaselineRenderer: "test",
		BaselineContract: 1,
	}); err != nil {
		t.Fatalf("create image part: %v", err)
	}

	parts, err := q.ListMessagePartsWithMediaByMessages(ctx, []string{messageID})
	if err != nil || len(parts) != 1 || parts[0].CtxMessagePart.Ordinal != 0 || parts[0].CtxMedium.ID != media.ID {
		t.Fatalf("batch part/media = %+v, %v", parts, err)
	}
	access := sqlc.GetMediaForSessionParams{
		MediaID: media.ID, UserID: user.String(), SessionID: sessionID,
		AgentID: pgtype.Text{String: agentID, Valid: true},
	}
	if got, err := q.GetMediaForSession(ctx, access); err != nil || got.ID != media.ID {
		t.Fatalf("scoped media = %+v, %v", got, err)
	}
	access.AgentID = pgtype.Text{String: "other-agent", Valid: true}
	if _, err := q.GetMediaForSession(ctx, access); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign agent media lookup error = %v, want ErrNoRows", err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM ctx_media WHERE id = $1`, media.ID); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	part, err := q.GetMessageParts(ctx, messageID)
	if err != nil || len(part) != 1 || part[0].MediaID.Valid || part[0].BaselineStatus != "ready" {
		t.Fatalf("part after media delete = %+v, %v; want baseline preserved and media unset", part, err)
	}
}

func seedUser(t *testing.T, db *pgxpool.Pool) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	if _, err := db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID.String(), userID.String()+"@test.local"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return userID
}
