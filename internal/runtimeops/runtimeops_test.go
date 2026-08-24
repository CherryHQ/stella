package runtimeops_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/runtimeops"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// TestRejectFifoResolvesPoisonHead drives the operator path end to end: a head
// that exhausted automatic retry is surfaced as such, the attributed rejection
// clears it and its content, and a platform redelivery of the rejected message
// deduplicates on identity instead of re-admitting.
func TestRejectFifoResolvesPoisonHead(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	q := sqlc.New(db)
	store := runtimeops.NewStore(db)
	if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{
		ID: "runtime-ops-ch", Name: "runtime-ops-ch", Type: pkgchannel.PlatformTelegram, AgentID: pgtype.Text{}, Enabled: true, Config: `{}`,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	payload := json.RawMessage(`[{"kind":"text","text":"poison"}]`)
	item, err := q.CreateChannelBindingFIFO(ctx, sqlc.CreateChannelBindingFIFOParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: "runtime-ops-ch", BindingKey: "binding-1",
		PrincipalID: "user-1", SourceKey: "message:poison", Kind: "message",
		Payload: payload, ImmutableMedia: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create fifo item: %v", err)
	}

	// Exhaust automatic retry: five claim/block rounds with zero backoff.
	for range 5 {
		if _, err := q.ClaimChannelBindingFIFOHead(ctx, item.ID); err != nil {
			t.Fatalf("claim head: %v", err)
		}
		if _, err := q.BlockChannelBindingFIFO(ctx, sqlc.BlockChannelBindingFIFOParams{
			ID: item.ID, Reason: "handler crashed", BackoffSeconds: 0,
		}); err != nil {
			t.Fatalf("block: %v", err)
		}
		if _, err := q.RetryBlockedChannelBindingFIFO(ctx, item.ID); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}

	live, err := store.ListFifo(ctx)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 || live[0].Status != "blocked" || !live[0].RetryExhausted {
		t.Fatalf("poison head not surfaced as retry-exhausted blocked: %+v", live)
	}

	rejected, err := store.RejectFifo(ctx, item.ID, "operator: unparseable payload", "op-test")
	if err != nil || !rejected {
		t.Fatalf("reject: rejected=%v err=%v", rejected, err)
	}
	assertFifoContentCleared(t, db, item.ID)

	if live, err = store.ListFifo(ctx); err != nil || len(live) != 0 {
		t.Fatalf("rejected item still occupies admission budget: %+v err=%v", live, err)
	}
	if rejected, err = store.RejectFifo(ctx, item.ID, "again", "op-test"); err != nil || rejected {
		t.Fatalf("re-reject of a terminal item must report false: rejected=%v err=%v", rejected, err)
	}

	// Redelivery of the rejected message carries the original content, which no
	// longer matches the cleared receipt; the terminal-status identity dedup
	// must still swallow it instead of erroring or re-admitting.
	replay, err := q.CreateChannelBindingFIFO(ctx, sqlc.CreateChannelBindingFIFOParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: "runtime-ops-ch", BindingKey: "binding-1",
		PrincipalID: "user-1", SourceKey: "message:poison", Kind: "message",
		Payload: payload, ImmutableMedia: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("redeliver rejected message: %v", err)
	}
	if replay.ID != item.ID || replay.Status != "rejected" {
		t.Fatalf("redelivery did not dedupe onto the terminal receipt: %+v", replay)
	}
}

func assertFifoContentCleared(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	var payload, media string
	var payloadBytes, attachmentBytes int64
	err := db.QueryRow(t.Context(),
		`SELECT payload::text, immutable_media::text, payload_bytes, attachment_bytes FROM channel_binding_fifo WHERE id = $1`, id,
	).Scan(&payload, &media, &payloadBytes, &attachmentBytes)
	if err != nil {
		t.Fatalf("read terminal row: %v", err)
	}
	if payload != "[]" || media != "[]" || attachmentBytes != 0 {
		t.Fatalf("terminal row keeps content: payload=%s media=%s attachment_bytes=%d", payload, media, attachmentBytes)
	}
}

// TestMarkSandboxDestroyedUnblocksGeneration proves the operator transition
// out of a permanently fenced generation: it leaves the live list and the
// session may mint its successor.
func TestMarkSandboxDestroyedUnblocksGeneration(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	q := sqlc.New(db)
	store := runtimeops.NewStore(db)
	const sessionID = "runtime-ops-session"
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID, Channel: "web", Kind: "chat",
		LastActive: time.Now().UTC(), AgentID: pgtype.Text{String: "agent", Valid: true},
		UserID: pgtype.Text{String: uuid.Must(uuid.NewV7()).String(), Valid: true},
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	bootID := uuid.Must(uuid.NewV7()).String()
	if _, err := q.CreateExecutorBoot(ctx, sqlc.CreateExecutorBootParams{ID: bootID}); err != nil {
		t.Fatalf("create boot: %v", err)
	}
	run, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID, ExecutorBootID: bootID,
		Source: "web", LeaseSeconds: 30,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	owner := pgtype.Text{String: bootID, Valid: true}
	runRef := pgtype.Text{String: run.ID, Valid: true}
	gen, err := q.CreateSessionSandboxGeneration(ctx, sqlc.CreateSessionSandboxGenerationParams{
		SessionID: sessionID, ExecutorBootID: owner, RunID: runRef,
		ResourceBackend: "process", ResourceID: "resource-1",
	})
	if err != nil {
		t.Fatalf("create generation: %v", err)
	}

	// Not fenced yet: the operator transition must refuse and name the state.
	if _, err := store.GetFencedSandbox(ctx, sessionID); err == nil {
		t.Fatal("GetFencedSandbox accepted a creating generation")
	}

	if _, err := q.FenceSessionSandboxGeneration(ctx, sqlc.FenceSessionSandboxGenerationParams{
		SessionID: sessionID, Generation: gen.Generation, ExecutorBootID: owner, RunID: runRef,
	}); err != nil {
		t.Fatalf("fence generation: %v", err)
	}

	live, err := store.ListSandbox(ctx)
	if err != nil || len(live) != 1 || live[0].State != "fenced" || live[0].FencedAt == nil {
		t.Fatalf("fenced generation not listed: %+v err=%v", live, err)
	}

	// While fenced, the successor generation must be refused.
	if _, err := q.CreateSessionSandboxGeneration(ctx, sqlc.CreateSessionSandboxGenerationParams{
		SessionID: sessionID, ExecutorBootID: owner, RunID: runRef,
		ResourceBackend: "process", ResourceID: "resource-2",
	}); err == nil {
		t.Fatal("successor generation minted while predecessor is fenced")
	}

	fenced, err := store.GetFencedSandbox(ctx, sessionID)
	if err != nil {
		t.Fatalf("get fenced: %v", err)
	}
	done, err := store.MarkSandboxDestroyed(ctx, sessionID, fenced.Generation)
	if err != nil || !done {
		t.Fatalf("mark destroyed: done=%v err=%v", done, err)
	}
	if live, err = store.ListSandbox(ctx); err != nil || len(live) != 0 {
		t.Fatalf("destroyed generation still listed live: %+v err=%v", live, err)
	}
	successor, err := q.CreateSessionSandboxGeneration(ctx, sqlc.CreateSessionSandboxGenerationParams{
		SessionID: sessionID, ExecutorBootID: owner, RunID: runRef,
		ResourceBackend: "process", ResourceID: "resource-2",
	})
	if err != nil {
		t.Fatalf("mint successor after operator destroy: %v", err)
	}
	if successor.Generation != gen.Generation+1 {
		t.Fatalf("successor generation = %d, want %d", successor.Generation, gen.Generation+1)
	}
}
