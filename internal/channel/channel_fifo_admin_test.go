package channel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestFIFOAdminRejectsBlockedItemReleasesQuotaAndMedia(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	item, mediaID, cost := seedFIFOAdminItem(t, db, "blocked", true)
	admin := NewFIFOAdmin(db)

	result, err := admin.Reject(ctx, item.ID, "operator@example.test", "payload is permanently malformed", true)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if !result.Changed || result.Audit == nil {
		t.Fatalf("Reject result = %+v, want changed audit", result)
	}
	if result.Record.Item.State != "rejected" || !result.Record.Item.ReleasedAt.Valid {
		t.Fatalf("rejected item = %+v", result.Record.Item)
	}
	if result.Audit.OperatorID != "operator@example.test" || result.Audit.Reason != "payload is permanently malformed" {
		t.Fatalf("audit = %+v", result.Audit)
	}

	var releasedRows, releasedBytes int64
	if err := db.QueryRow(ctx, `SELECT released_rows, released_bytes FROM channel_deployment_quota WHERE channel_id = 'deployment'`).Scan(&releasedRows, &releasedBytes); err != nil {
		t.Fatalf("deployment quota: %v", err)
	}
	if releasedRows != 1 || releasedBytes != cost {
		t.Fatalf("deployment release = (%d, %d), want (1, %d)", releasedRows, releasedBytes, cost)
	}
	var mediaRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_fifo_media WHERE item_id = $1`, item.ID).Scan(&mediaRows); err != nil {
		t.Fatalf("media references: %v", err)
	}
	if mediaRows != 0 {
		t.Fatalf("channel FIFO media rows = %d, want 0", mediaRows)
	}
	// The RESTRICT edge is the reason terminal cleanup removes the link first.
	if _, err := db.Exec(ctx, `DELETE FROM ctx_media WHERE id = $1`, mediaID); err != nil {
		t.Fatalf("delete now-unreferenced ctx_media: %v", err)
	}

	again, err := admin.Reject(ctx, item.ID, "second-operator", "duplicate attempt", true)
	if err != nil {
		t.Fatalf("idempotent Reject: %v", err)
	}
	if again.Changed || again.Audit != nil {
		t.Fatalf("second Reject = %+v, want unchanged terminal receipt", again)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_fifo_rejection WHERE item_id = $1`, item.ID).Scan(&audits); err != nil {
		t.Fatalf("rejection audit count: %v", err)
	}
	if audits != 1 {
		t.Fatalf("rejection audit count = %d, want 1", audits)
	}
}

func TestFIFOAdminRefusesPendingAndLiveRun(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	pending, _, _ := seedFIFOAdminItem(t, db, "pending", false)
	admin := NewFIFOAdmin(db)
	if _, err := admin.Reject(ctx, pending.ID, "operator", "too early", true); !errors.Is(err, ErrChannelFIFOAdminNotBlocked) {
		t.Fatalf("pending Reject error = %v, want ErrChannelFIFOAdminNotBlocked", err)
	}

	live, _, _ := seedFIFOAdminItem(t, db, "blocked", false)
	q := sqlc.New(db)
	bootID := uuid.Must(uuid.NewV7()).String()
	if _, err := q.CreateExecutorBoot(ctx, bootID); err != nil {
		t.Fatalf("CreateExecutorBoot: %v", err)
	}
	userID := uuid.Must(uuid.NewV7()).String()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email, name) VALUES ($1, $2, $3)`, userID, userID+"@example.test", "test user"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: "fifo-admin-live-session-" + live.ID,
		Channel: "test", Kind: "chat", LastActive: time.Now().UTC(),
		AgentID: pgtype.Text{String: "agent", Valid: true}, UserID: pgtype.Text{String: userID, Valid: true},
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	run, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: "fifo-admin-live-session-" + live.ID,
		ExecutorBootID: bootID, Source: "channel", LeaseSeconds: 30,
	})
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel_fifo_item SET run_id = $1 WHERE id = $2`, run.ID, live.ID); err != nil {
		t.Fatalf("link live run: %v", err)
	}
	if _, err := admin.Reject(ctx, live.ID, "operator", "live run", true); !errors.Is(err, ErrChannelFIFOAdminLiveRun) {
		t.Fatalf("live-run Reject error = %v, want ErrChannelFIFOAdminLiveRun", err)
	}
}

func TestFIFOAdminRequiresForceAndReason(t *testing.T) {
	db := dbtest.New(t)
	item, _, _ := seedFIFOAdminItem(t, db, "blocked", false)
	admin := NewFIFOAdmin(db)
	if _, err := admin.Reject(t.Context(), item.ID, "operator", "reason", false); !errors.Is(err, ErrChannelFIFOAdminForceRequired) {
		t.Fatalf("without force error = %v, want ErrChannelFIFOAdminForceRequired", err)
	}
	if _, err := admin.Reject(t.Context(), item.ID, "operator", "  ", true); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("empty reason error = %v, want reason validation", err)
	}
}

func TestFIFOAdminListReturnsBoundedPayloadFreeSummaries(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	blocked, _, _ := seedFIFOAdminItem(t, db, "blocked", false)
	_, _, _ = seedFIFOAdminItem(t, db, "pending", false)
	admin := NewFIFOAdmin(db)

	summaries, err := admin.ListBlocked(ctx, 1)
	if err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != blocked.ID {
		t.Fatalf("ListBlocked summaries = %+v, want blocked item %s", summaries, blocked.ID)
	}
	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	if strings.Contains(string(encoded), `"payload"`) || strings.Contains(string(encoded), "admin-test") {
		t.Fatalf("list summary leaked payload: %s", encoded)
	}
	if _, err := admin.ListBlocked(ctx, DefaultFIFOAdminListLimit+1); err == nil {
		t.Fatal("ListBlocked accepted a limit above the bounded maximum")
	}
}

func seedFIFOAdminItem(t *testing.T, db *pgxpool.Pool, state string, withMedia bool) (sqlc.ChannelFifoItem, string, int64) {
	// This helper is intentionally kept below the service tests; production
	// maintenance never fabricates quota rows or bypasses admission checks.
	t.Helper()
	ctx := t.Context()
	q := sqlc.New(db)
	channelID := "fifo-admin-" + uuid.Must(uuid.NewV7()).String()
	if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{ID: channelID, Name: channelID, Type: "test", Enabled: true, Config: `{}`}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	bindingID := uuid.Must(uuid.NewV7()).String()
	principalID := uuid.Must(uuid.NewV7()).String()
	principalKey := "sender:" + principalID
	if err := q.EnsureChannelBinding(ctx, sqlc.EnsureChannelBindingParams{
		ID: bindingID, ChannelID: channelID, Platform: "test", ChatKey: "chat-1",
		PrincipalKind: "sender", PrincipalID: principalID,
	}); err != nil {
		t.Fatalf("EnsureChannelBinding: %v", err)
	}
	binding, err := q.GetChannelBinding(ctx, bindingID)
	if err != nil {
		t.Fatalf("GetChannelBinding: %v", err)
	}
	if _, err := q.EnsureChannelDeploymentQuota(ctx, sqlc.EnsureChannelDeploymentQuotaParams{
		ChannelID: channelDeploymentQuotaKey, MaxRows: channelDeploymentDefaultRows, MaxBytes: channelDeploymentDefaultBytes,
	}); err != nil {
		t.Fatalf("EnsureChannelDeploymentQuota: %v", err)
	}
	if _, err := q.EnsureChannelPrincipalQuota(ctx, sqlc.EnsureChannelPrincipalQuotaParams{
		PrincipalKey: principalKey, PrincipalKind: "sender", PrincipalID: principalID,
		MaxRows: channelPrincipalDefaultRows, MaxBytes: channelPrincipalDefaultBytes,
	}); err != nil {
		t.Fatalf("EnsureChannelPrincipalQuota: %v", err)
	}
	payload := json.RawMessage(`{"kind":"admin-test"}`)
	var mediaID string
	var mediaBytes int64
	if withMedia {
		if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email, name) VALUES ($1, $2, $3)`, principalID, principalID+"@example.test", "FIFO admin test"); err != nil {
			t.Fatalf("insert media owner: %v", err)
		}
		media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
			UserID: pgtype.Text{String: principalID, Valid: true}, Sha256: []byte(strings.Repeat("a", 32)),
			MimeType: "image/png", SizeBytes: 7,
		})
		if err != nil {
			t.Fatalf("CreateMediaIfAbsent: %v", err)
		}
		mediaID, mediaBytes = media.ID, media.SizeBytes
	}
	cost := int64(len(payload)) + mediaBytes
	for name, reserve := range map[string]func() (int64, error){
		"deployment": func() (int64, error) {
			return q.ReserveChannelDeploymentQuota(ctx, sqlc.ReserveChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, ByteCost: cost})
		},
		"principal": func() (int64, error) {
			return q.ReserveChannelPrincipalQuota(ctx, sqlc.ReserveChannelPrincipalQuotaParams{PrincipalKey: principalKey, ByteCost: cost})
		},
		"binding": func() (int64, error) {
			return q.ReserveChannelBindingQuota(ctx, sqlc.ReserveChannelBindingQuotaParams{BindingID: binding.ID, ByteCost: cost})
		},
	} {
		if rows, err := reserve(); err != nil || rows != 1 {
			t.Fatalf("reserve %s: rows=%d err=%v", name, rows, err)
		}
	}
	item, err := q.InsertChannelFIFOItem(ctx, sqlc.InsertChannelFIFOItemParams{
		ID: uuid.Must(uuid.NewV7()).String(), BindingID: binding.ID, PrincipalKey: principalKey,
		Seq: 1, SourceKey: "source:" + uuid.Must(uuid.NewV7()).String(), SchemaVersion: 1,
		Payload: payload, PayloadBytes: int64(len(payload)), MediaBytes: mediaBytes,
		ByteCost: cost, Command: "chat", ExpectedBindingRevision: binding.Revision,
	})
	if err != nil {
		t.Fatalf("InsertChannelFIFOItem: %v", err)
	}
	if mediaID != "" {
		if _, err := q.AddChannelFIFOMedia(ctx, sqlc.AddChannelFIFOMediaParams{
			ItemID: item.ID, MediaID: mediaID, MimeType: "image/png", SizeBytes: mediaBytes,
		}); err != nil {
			t.Fatalf("AddChannelFIFOMedia: %v", err)
		}
	}
	if state != "pending" {
		if _, err := db.Exec(ctx, `UPDATE channel_fifo_item SET state = $1 WHERE id = $2`, state, item.ID); err != nil {
			t.Fatalf("set FIFO state: %v", err)
		}
		item.State = state
	}
	return item, mediaID, cost
}
