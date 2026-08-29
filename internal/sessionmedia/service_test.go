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
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestPipelineEnrichAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewPipeline(
		assets.SessionMedia(),
		db,
		&fakeSnapshotLoader{err: errors.New("settings unavailable")},
		func(_, _, _ string) (providers.StreamFunc, error) {
			t.Fatal("snapshot failure must use local baseline fallback")
			return nil, nil
		},
		PipelineOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	owner := seedUser(t, db)
	raw := imageBlock(t, 42)
	blocks, err := pipeline.Enrich(ctx, UserOwner(owner), "agent-1", []ai.ContentBlock{
		ai.TextContent{Text: "inspect this"},
		raw,
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("enriched blocks = %#v", blocks)
	}
	ref, ok := blocks[1].(ai.ImageRefContent)
	if !ok || ref.MediaID == "" || ref.Baseline.Projection() == "" {
		t.Fatalf("canonical image ref = %#v", blocks[1])
	}
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			t.Fatalf("canonical output leaked raw image: %#v", blocks)
		}
	}

	loaded, err := pipeline.Load(ctx, UserOwner(owner), ref.MediaID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(loaded.Data)
	if err != nil {
		t.Fatalf("decode loaded image: %v", err)
	}
	want, err := base64.StdEncoding.DecodeString(raw.Data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MimeType != raw.MimeType || !bytes.Equal(got, want) {
		t.Fatalf("loaded image changed: mime=%q bytes_equal=%t", loaded.MimeType, bytes.Equal(got, want))
	}
	if _, err := pipeline.Load(ctx, UserOwner(uuid.New()), ref.MediaID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign load error = %v, want ErrNotFound", err)
	}

	var mediaRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_media WHERE user_id = $1`, owner).Scan(&mediaRows); err != nil {
		t.Fatal(err)
	}
	if mediaRows != 1 {
		t.Fatalf("ctx_media rows = %d, want 1", mediaRows)
	}
}

func TestPersistDeduplicatesPerUserAndSeparatesUsers(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	home := t.TempDir()
	assets, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatalf("new asset store: %v", err)
	}
	svc, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	userA := seedUser(t, db)
	userB := seedUser(t, db)
	in := Input{
		Owner:    UserOwner(userA),
		Data:     []byte("same immutable image bytes"),
		MimeType: "image/png",
	}
	first, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("first Persist: %v", err)
	}
	second, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("second Persist: %v", err)
	}
	if first != second {
		t.Fatalf("same-user duplicate IDs = %q, %q; want one row", first, second)
	}
	mediaID, err := uuid.Parse(first)
	if err != nil || mediaID.Version() != 7 {
		t.Fatalf("database-generated media ID = %q (version %d), parse error = %v; want UUIDv7", first, mediaID.Version(), err)
	}

	in.Owner = UserOwner(userB)
	otherUser, err := svc.Persist(ctx, in)
	if err != nil {
		t.Fatalf("cross-user Persist: %v", err)
	}
	if otherUser == first {
		t.Fatalf("cross-user media reused metadata: first=%+v other=%+v", first, otherUser)
	}

	digest := sha256.Sum256(in.Data)
	for _, userID := range []uuid.UUID{userA, userB} {
		path := filepath.Join(home, "users", userID.String(), "session-media", hex.EncodeToString(digest[:]))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("user %s session-media object missing at its isolated key: %v", userID, err)
		}
	}
	got, err := assets.SessionMedia().OpenSessionMedia(ctx, asset.UserMediaOwner(userB), digest, int64(len(in.Data)))
	if err != nil || string(got) != string(in.Data) {
		t.Fatalf("open cross-user object = %q, %v", got, err)
	}
	if _, err := assets.SessionMedia().OpenSessionMedia(ctx, asset.UserMediaOwner(userA), digest, int64(len(in.Data))); err != nil {
		t.Fatalf("open same-user object: %v", err)
	}

	in.Owner = UserOwner(userA)
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
	svc, err := newMediaStore(assets.SessionMedia(), db)
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
	stored, err := svc.Persist(ctx, Input{Owner: UserOwner(owner), Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load(ctx, UserOwner(owner), stored)
	if err != nil || loaded.MimeType != "image/png" || loaded.Data == "" {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
	if _, err := svc.Load(ctx, UserOwner(other), stored); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign load = %v, want opaque ErrNotFound", err)
	}
	if _, err := svc.Load(ctx, UserOwner(owner), "not-a-media-id"); !errors.Is(err, ErrNotFound) {
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
	svc, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		t.Fatal(err)
	}

	data := noisyPNG(t, 1400, 1400)
	if len(data) <= vision.MaxRendererPayloadBytes {
		t.Fatalf("fixture = %d bytes, want more than provider payload ceiling", len(data))
	}
	owner := seedUser(t, db)
	stored, err := svc.Persist(ctx, Input{Owner: UserOwner(owner), Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	persisted, err := assets.SessionMedia().OpenSessionMedia(ctx, asset.UserMediaOwner(owner), digest, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persisted, data) {
		t.Fatal("immutable asset bytes changed before history/provider projection")
	}

	loaded, err := svc.Load(ctx, UserOwner(owner), stored)
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
	persisted, err = assets.SessionMedia().OpenSessionMedia(ctx, asset.UserMediaOwner(owner), digest, int64(len(data)))
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
	svc, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	user := seedUser(t, db)
	const agentID = "session-media-agent"
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Media Agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	media, err := svc.Persist(ctx, Input{
		Owner: UserOwner(user), Data: []byte("media bytes"), MimeType: "image/png",
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
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type)
		VALUES ($1, $2, 1, 'user', 'text', 'baseline text', 2, $3)`, messageID, conversationID, eventlog.ActorHuman); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID:          uuid.NewString(),
		MessageID:   messageID,
		PartType:    "image",
		Ordinal:     0,
		MediaID:     pgtype.Text{String: media, Valid: true},
		TextContent: pgtype.Text{String: "stored baseline", Valid: true},
	}); err != nil {
		t.Fatalf("create image part: %v", err)
	}

	parts, err := q.ListMessagePartsWithMediaByMessages(ctx, []string{messageID})
	if err != nil || len(parts) != 1 || parts[0].CtxMessagePart.Ordinal != 0 || parts[0].CtxMedium.ID != media {
		t.Fatalf("batch part/media = %+v, %v", parts, err)
	}
	access := sqlc.GetMediaForSessionParams{
		MediaID:   media,
		OwnerKind: pgtype.Text{String: "user", Valid: true},
		OwnerID:   pgtype.Text{String: user.String(), Valid: true},
		SessionID: sessionID,
		AgentID:   pgtype.Text{String: agentID, Valid: true},
	}
	if got, err := q.GetMediaForSession(ctx, access); err != nil || got.ID != media {
		t.Fatalf("scoped media = %+v, %v", got, err)
	}
	access.AgentID = pgtype.Text{String: "other-agent", Valid: true}
	if _, err := q.GetMediaForSession(ctx, access); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign agent media lookup error = %v, want ErrNoRows", err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM ctx_media WHERE id = $1`, media); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	part, err := q.GetMessageParts(ctx, messageID)
	if err != nil || len(part) != 1 || part[0].MediaID.Valid || part[0].TextContent.String != "stored baseline" {
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

func seedGroup(t *testing.T, db *pgxpool.Pool) uuid.UUID {
	t.Helper()
	groupID := uuid.New()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES ($1, 'test', $2)`,
		groupID.String(), groupID.String()); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	return groupID
}

// A group owns media exactly like a user does. The same bytes stored for both
// are two rows and two objects, and neither owner can read the other's, which
// is what lets a group session carry canonical images at all.
func TestPersistAndLoadAreOwnerScopedAcrossKinds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	home := t.TempDir()
	assets, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		t.Fatal(err)
	}

	user := UserOwner(seedUser(t, db))
	group := GroupOwner(seedGroup(t, db))
	fixture := imageBlock(t, 7)
	data, err := base64.StdEncoding.DecodeString(fixture.Data)
	if err != nil {
		t.Fatal(err)
	}

	userMedia, err := svc.Persist(ctx, Input{Owner: user, Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatalf("persist user media: %v", err)
	}
	groupMedia, err := svc.Persist(ctx, Input{Owner: group, Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatalf("persist group media: %v", err)
	}
	if userMedia == groupMedia {
		t.Fatal("identical bytes collapsed two owners onto one media row")
	}
	if again, err := svc.Persist(ctx, Input{Owner: group, Data: data, MimeType: "image/png"}); err != nil || again != groupMedia {
		t.Fatalf("group re-persist = %q, %v; want the same row", again, err)
	}

	digest := sha256.Sum256(data)
	groupPath := filepath.Join(home, "groups", group.ID.String(), "session-media", hex.EncodeToString(digest[:]))
	if _, err := os.Stat(groupPath); err != nil {
		t.Fatalf("group object missing at its own prefix: %v", err)
	}

	if _, err := svc.Load(ctx, group, groupMedia); err != nil {
		t.Fatalf("group load: %v", err)
	}
	if _, err := svc.Load(ctx, user, groupMedia); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user resolved group media: %v", err)
	}
	if _, err := svc.Load(ctx, group, userMedia); !errors.Is(err, ErrNotFound) {
		t.Fatalf("group resolved user media: %v", err)
	}

	var kinds int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM ctx_media WHERE (user_id = $1 AND group_id IS NULL) OR (group_id = $2 AND user_id IS NULL)`,
		user.ID.String(), group.ID.String()).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != 2 {
		t.Fatalf("owner columns = %d rows, want exactly one per owner kind", kinds)
	}
}

// Owner identity is (kind, id), not id alone. uuidv7 makes a user/group
// collision vanishingly unlikely, but the schema is what guarantees the
// separation, so it is asserted against the one case that would break a
// COALESCE-only key: the same UUID owning the same bytes as both kinds.
func TestOwnerIdentityIncludesKindWhenIDsCollide(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		t.Fatal(err)
	}

	shared := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`,
		shared.String(), shared.String()+"@test.local"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES ($1, 'test', $2)`,
		shared.String(), shared.String()); err != nil {
		t.Fatalf("seed group with the same id: %v", err)
	}

	user, group := UserOwner(shared), GroupOwner(shared)
	fixture := imageBlock(t, 11)
	data, err := base64.StdEncoding.DecodeString(fixture.Data)
	if err != nil {
		t.Fatal(err)
	}

	userMedia, err := svc.Persist(ctx, Input{Owner: user, Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatalf("persist user media: %v", err)
	}
	groupMedia, err := svc.Persist(ctx, Input{Owner: group, Data: data, MimeType: "image/png"})
	if err != nil {
		t.Fatalf("persist group media: %v", err)
	}
	if userMedia == groupMedia {
		t.Fatal("same UUID as user and group collapsed onto one media row")
	}

	if _, err := svc.Load(ctx, user, groupMedia); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user read the group row that shares its UUID: %v", err)
	}
	if _, err := svc.Load(ctx, group, userMedia); !errors.Is(err, ErrNotFound) {
		t.Fatalf("group read the user row that shares its UUID: %v", err)
	}
	if _, err := svc.Load(ctx, user, userMedia); err != nil {
		t.Fatalf("user load: %v", err)
	}
	if _, err := svc.Load(ctx, group, groupMedia); err != nil {
		t.Fatalf("group load: %v", err)
	}

	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_media WHERE owner_id = $1`, shared.String()).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows for the shared UUID = %d, want one per owner kind", rows)
	}
}

// Group ingestion pays for storage, not for description: Persist mints a
// reference with no baseline, and RenderBaselines fills it in later, once.
func TestPersistDefersBaselineUntilRender(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	renders := 0
	pipeline, err := newTestPipeline(t, assets, db, func() vision.BaselineRenderer {
		renders++
		return stubRenderer{baseline: validBaseline()}
	})
	if err != nil {
		t.Fatal(err)
	}
	group := GroupOwner(seedGroup(t, db))
	raw := imageBlock(t, 11)

	persisted, err := pipeline.Persist(ctx, group, []ai.ContentBlock{ai.TextContent{Text: "look"}, raw})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	ref, ok := persisted[1].(ai.ImageRefContent)
	if !ok || ref.MediaID == "" {
		t.Fatalf("persisted block = %#v, want a bare reference", persisted[1])
	}
	if ref.Baseline.Text != "" {
		t.Fatalf("ingestion rendered a baseline: %q", ref.Baseline.Text)
	}
	if renders != 0 {
		t.Fatalf("ingestion resolved %d renderers, want none", renders)
	}
	// The projection must stay non-empty without a baseline: group triage drops
	// an empty message, so a bare reference would silence a pure-image post.
	if ai.FlattenCanonicalText(persisted) == "" {
		t.Fatal("bare reference projected to empty text")
	}

	rendered, err := pipeline.RenderBaselines(ctx, group, "agent-1", persisted)
	if err != nil {
		t.Fatalf("render baselines: %v", err)
	}
	filled, ok := rendered[1].(ai.ImageRefContent)
	if !ok || filled.MediaID != ref.MediaID || filled.Baseline.Text != validBaseline().Text {
		t.Fatalf("rendered block = %#v", rendered[1])
	}

	// A reference that already carries a baseline is never rendered again.
	renders = 0
	if _, err := pipeline.RenderBaselines(ctx, group, "agent-1", rendered); err != nil {
		t.Fatalf("second render: %v", err)
	}
	if renders != 0 {
		t.Fatalf("re-rendered an already described image %d times", renders)
	}

	// A foreign owner cannot render, and says nothing about why.
	other := GroupOwner(seedGroup(t, db))
	out, err := pipeline.RenderBaselines(ctx, other, "agent-1", persisted)
	if err != nil {
		t.Fatalf("foreign render: %v", err)
	}
	if foreign, ok := out[1].(ai.ImageRefContent); !ok || foreign.Baseline.Text != "" {
		t.Fatalf("foreign owner rendered a baseline: %#v", out[1])
	}
}

type stubRenderer struct {
	baseline ai.ImageBaseline
	err      error
}

func (r stubRenderer) Baseline(context.Context, vision.Request) (ai.ImageBaseline, error) {
	return r.baseline, r.err
}

func newTestPipeline(t *testing.T, assets *asset.Store, db *pgxpool.Pool, renderer func() vision.BaselineRenderer) (*Pipeline, error) {
	t.Helper()
	media, err := newMediaStore(assets.SessionMedia(), db)
	if err != nil {
		return nil, err
	}
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer()
	}), PipelineOptions{})
	if err != nil {
		return nil, err
	}
	return &Pipeline{media: media, enricher: enricher}, nil
}
