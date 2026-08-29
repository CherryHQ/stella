package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The sweep exists because a group image is persisted before the message that
// would reference it, so an abandoned delivery leaves a row and a blob nothing
// points at. Every survivor here is a way of being referenced, plus the 24-hour
// window that ordinary ingestion needs.
func TestOrphanSweepDeletesOnlyUnreachableMedia(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	home := t.TempDir()
	assets, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := newTestPipeline(t, assets, db, func() vision.BaselineRenderer { return stubRenderer{baseline: validBaseline()} })
	if err != nil {
		t.Fatal(err)
	}
	store := pipeline.media
	user := UserOwner(seedUser(t, db))
	group := GroupOwner(seedGroup(t, db))

	persist := func(owner Owner, body string) (string, [sha256.Size]byte) {
		t.Helper()
		id, _, err := store.Persist(ctx, Input{Owner: owner, Data: []byte(body), MimeType: "image/png"})
		if err != nil {
			t.Fatalf("persist %q: %v", body, err)
		}
		return id, sha256.Sum256([]byte(body))
	}

	partReferenced, partDigest := persist(user, "referenced by a message part")
	groupReferenced, groupDigest := persist(group, "referenced by a group block")
	orphan, orphanDigest := persist(user, "referenced by nothing")
	fresh, freshDigest := persist(user, "too young to sweep")

	seedImagePart(t, db, user, partReferenced)
	seedGroupImageBlock(t, db, group, groupReferenced)
	// Everything but `fresh` predates the ingestion window.
	for _, id := range []string{partReferenced, groupReferenced, orphan} {
		if _, err := db.Exec(ctx, `UPDATE ctx_media SET created_at = now() - interval '48 hours' WHERE id = $1`, id); err != nil {
			t.Fatalf("age media: %v", err)
		}
	}

	deleted, err := pipeline.sweepOnce(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("swept %d rows, want only the orphan", deleted)
	}

	assertMediaRow(t, db, partReferenced, true)
	assertMediaRow(t, db, groupReferenced, true)
	assertMediaRow(t, db, fresh, true)
	assertMediaRow(t, db, orphan, false)

	media := assets.SessionMedia()
	if _, err := media.OpenSessionMedia(ctx, user, orphanDigest, int64(len("referenced by nothing"))); err == nil {
		t.Fatal("orphan blob survived the sweep")
	}
	for _, kept := range []struct {
		owner  Owner
		digest [sha256.Size]byte
		size   int
	}{
		{user, partDigest, len("referenced by a message part")},
		{group, groupDigest, len("referenced by a group block")},
		{user, freshDigest, len("too young to sweep")},
	} {
		if _, err := media.OpenSessionMedia(ctx, kept.owner, kept.digest, int64(kept.size)); err != nil {
			t.Fatalf("referenced blob lost: %v", err)
		}
	}

	// A second sweep has nothing left to do, and says so rather than churning.
	if deleted, err := pipeline.sweepOnce(ctx); err != nil || deleted != 0 {
		t.Fatalf("second sweep = %d, %v; want a clean no-op", deleted, err)
	}
}

func assertMediaRow(t *testing.T, db *pgxpool.Pool, mediaID string, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM ctx_media WHERE id = $1)`, mediaID).Scan(&exists); err != nil {
		t.Fatalf("read media row: %v", err)
	}
	if exists != want {
		t.Fatalf("media %s exists = %t, want %t", mediaID, exists, want)
	}
}

func seedImagePart(t *testing.T, db *pgxpool.Pool, owner Owner, mediaID string) {
	t.Helper()
	ctx := context.Background()
	agentID := "sweep-agent-" + uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Sweep Agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	conversationID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id)
		VALUES ($1, $2, 'web', 'chat', $3, $4)`, conversationID, uuid.NewString(), agentID, owner.ID.String()); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	messageID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type)
		VALUES ($1, $2, 1, 'user', 'text', '', 0, $3)`, messageID, conversationID, eventlog.ActorHuman); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := sqlc.New(db).CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID:        uuid.NewString(),
		MessageID: messageID,
		PartType:  "image",
		Ordinal:   0,
		MediaID:   pgtype.Text{String: mediaID, Valid: true},
	}); err != nil {
		t.Fatalf("seed image part: %v", err)
	}
}

func seedGroupImageBlock(t *testing.T, db *pgxpool.Pool, owner Owner, mediaID string) {
	t.Helper()
	blocks := `[{"kind":"text","text":"look"},{"kind":"image_ref","media_id":"` + mediaID + `"}]`
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_group_message (group_id, seq, actor_type, actor_id, content_blocks)
		VALUES ($1, 1, 'user', 'u-1', $2::jsonb)`, owner.ID.String(), blocks); err != nil {
		t.Fatalf("seed group message: %v", err)
	}
}

// PurgeOwner is the delete path owner deletion calls. It maps only the two
// principals that own media; an agent has none of its own.
func TestPurgeOwnerMapsPrincipalsAndIgnoresAgents(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := newTestPipeline(t, assets, db, func() vision.BaselineRenderer { return stubRenderer{baseline: validBaseline()} })
	if err != nil {
		t.Fatal(err)
	}
	user := UserOwner(seedUser(t, db))
	fixture := imageBlock(t, 23)
	data, err := base64.StdEncoding.DecodeString(fixture.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pipeline.media.Persist(ctx, Input{Owner: user, Data: data, MimeType: "image/png"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	digest := sha256.Sum256(data)

	if err := pipeline.PurgeOwner(ctx, home.OwnerAgent, "some-agent"); err != nil {
		t.Fatalf("agent purge: %v", err)
	}
	if _, err := assets.SessionMedia().OpenSessionMedia(ctx, user, digest, int64(len(data))); err != nil {
		t.Fatalf("agent purge touched user media: %v", err)
	}
	if err := pipeline.PurgeOwner(ctx, home.OwnerUser, "not-a-uuid"); err == nil {
		t.Fatal("malformed owner id accepted")
	}
	if err := pipeline.PurgeOwner(ctx, home.OwnerUser, user.ID.String()); err != nil {
		t.Fatalf("user purge: %v", err)
	}
	if _, err := assets.SessionMedia().OpenSessionMedia(ctx, user, digest, int64(len(data))); err == nil {
		t.Fatal("user media survived its owner's purge")
	}
}

// The drain loop is the sweep's whole cost control: a full round means there is
// more to do, a short round means the backlog is gone, and the round cap keeps
// one firing from scanning the group message table indefinitely.
func TestOrphanSweepDrainLoopBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		round      func(int) (int, error)
		wantRounds int
	}{
		{
			name:       "every round full stops at the cap",
			round:      func(int) (int, error) { return orphanSweepBatch, nil },
			wantRounds: maxRoundsPerSweep,
		},
		{
			name: "a round one short of the batch ends the sweep",
			round: func(call int) (int, error) {
				if call == 1 {
					return orphanSweepBatch, nil
				}
				return orphanSweepBatch - 1, nil
			},
			wantRounds: 2,
		},
		{
			name:       "an empty round ends the sweep immediately",
			round:      func(int) (int, error) { return 0, nil },
			wantRounds: 1,
		},
		{
			name:       "a failure stops the firing and waits for the next tick",
			round:      func(int) (int, error) { return 0, errors.New("database unavailable") },
			wantRounds: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rounds := 0
			worker := &orphanSweepWorker{sweep: func(context.Context) (int, error) {
				rounds++
				return tc.round(rounds)
			}}
			if err := worker.Work(context.Background(), nil); err != nil {
				t.Fatalf("Work returned %v, want nil: a sweep never fails its job", err)
			}
			if rounds != tc.wantRounds {
				t.Fatalf("rounds = %d, want %d", rounds, tc.wantRounds)
			}
		})
	}
}
