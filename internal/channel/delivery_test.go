package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type deliveryFixture struct {
	db     *pgxpool.Pool
	store  *agentrun.Store
	lease  *agentrun.Lease
	target deliveryTarget
}

func newDeliveryFixture(t *testing.T) deliveryFixture {
	t.Helper()
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, sessionID, "channel:test")
	if err != nil {
		t.Fatal(err)
	}
	return deliveryFixture{
		db: db, store: store, lease: lease,
		target: deliveryTarget{
			Platform: pkgchannel.PlatformTelegram, ChannelID: "telegram-main",
			ChatID: "chat-1", ThreadID: "topic-1", ReplyTo: "msg-1",
		},
	}
}

func (f deliveryFixture) newDelivery(t *testing.T) *runDelivery {
	t.Helper()
	delivery, err := createRunDelivery(t.Context(), f.db, f.lease, f.target)
	if err != nil {
		t.Fatalf("create delivery record: %v", err)
	}
	if delivery == nil {
		t.Fatal("delivery handle was not created for an addressable target")
	}
	return delivery
}

func (f deliveryFixture) output(t *testing.T) sqlc.AgentRunOutput {
	t.Helper()
	row, err := sqlc.New(f.db).GetAgentRunOutput(t.Context(), f.lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read delivery record: %v", err)
	}
	return row
}

// A stalled or failed channel send must not keep execution ownership: the Run is
// terminal as soon as its output is committed, and the delivery attempt settles
// after.
func TestCommittedOutputEndsTheRunBeforeAnySend(t *testing.T) {
	f := newDeliveryFixture(t)
	// Admission creates the record; the commit below publishes it.
	delivery := f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCompleted, Reason: "turn completed",
		Output: agentruntime.TurnOutput{Text: "hello"},
	}

	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatalf("commit handoff: %v", err)
	}
	run, err := sqlc.New(f.db).GetAgentRun(t.Context(), f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentrun.StatusCompleted {
		t.Fatalf("run status = %q, want completed without waiting for the platform", run.Status)
	}
	output := f.output(t)
	if output.State != "ready" || output.Text != "hello" {
		t.Fatalf("output = state=%q text=%q, want ready/hello", output.State, output.Text)
	}
	if output.AttemptAt.Valid {
		t.Fatal("a reply that never attempted a send was recorded as attempted")
	}
	// The platform send now runs on committed output, and the attempt is durable.
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatalf("authorize the committed reply: %v", err)
	}
	attempted := f.output(t)
	if !attempted.AttemptAt.Valid {
		t.Fatal("send was authorized without a durable attempt marker")
	}
}

// A failed result handoff must never leave a completed Run without its required
// output record.
func TestFailedHandoffCannotReportCompletedRun(t *testing.T) {
	f := newDeliveryFixture(t)
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCompleted, Reason: "turn completed",
		Output: agentruntime.TurnOutput{Text: "hello"},
	}
	if _, err := f.db.Exec(t.Context(), "DELETE FROM agent_run_output WHERE run_id = $1", f.lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	// The record vanished underneath the commit, which is exactly the handoff
	// failure this contract must not report as success.
	err := commitTurnHandoff(t.Context(), f.lease, outcome, true)
	if err == nil {
		t.Fatal("handoff with no delivery record reported success")
	}
	run, readErr := sqlc.New(f.db).GetAgentRun(t.Context(), f.lease.Guard.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if run.Status == agentrun.StatusCompleted {
		t.Fatalf("run status = %q, want a failed Run (never completed without its output record)", run.Status)
	}
	var count int
	if err := f.db.QueryRow(t.Context(), "SELECT count(*) FROM agent_run_output WHERE run_id = $1", f.lease.Guard.RunID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("output records = %d, want none for a failed handoff", count)
	}
}

// A durable stop request that wins the terminal transition must not publish the
// reply for delivery.
func TestAbortOutranksTheHandoffSoNothingIsPublished(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCompleted, Reason: "turn completed",
		Output: agentruntime.TurnOutput{Text: "hello"},
	}
	if _, err := f.store.RequestAbort(t.Context(), f.lease.Guard.SessionID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatalf("commit handoff after a stop request: %v", err)
	}
	run, err := sqlc.New(f.db).GetAgentRun(t.Context(), f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentrun.StatusAborted {
		t.Fatalf("run status = %q, want aborted", run.Status)
	}
	if output := f.output(t); output.State != "live" {
		t.Fatalf("output state = %q, want live (an aborted turn publishes nothing)", output.State)
	}
	// And the aborted turn's model output can no longer be sent.
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); !errors.Is(err, errDeliveryUnauthorized) {
		t.Fatalf("output send after abort = %v, want a refusal", err)
	}
	// Its channel-owned status message still can, so the user learns what happened.
	if err := delivery.Authorize(t.Context(), pkgchannel.SendControl); err != nil {
		t.Fatalf("control send after abort = %v, want authorized", err)
	}
}

// The durable attempt marker and the per-send decision are one statement, so a
// live send can never commit an unattempted reply record afterwards.
func TestLiveSendIsRecordedBeforeTheReplyCommits(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)

	// A live delta is authorized while execution ownership is valid.
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatalf("live delta: %v", err)
	}
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCompleted, Reason: "turn completed",
		Output: agentruntime.TurnOutput{Text: "hello"},
	}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatalf("commit handoff: %v", err)
	}
	output := f.output(t)
	if output.State != "ready" || !output.AttemptAt.Valid {
		t.Fatalf("output = state=%q attempted=%v, want ready with a recorded attempt", output.State, output.AttemptAt.Valid)
	}
}

// A send whose attempt marker cannot be written must not happen.
func TestSendRefusedWhenTheAttemptMarkerCannotBeWritten(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	if _, err := f.db.Exec(t.Context(), "DELETE FROM agent_run_output WHERE run_id = $1", f.lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); !errors.Is(err, errDeliveryUnauthorized) {
		t.Fatalf("authorize without a record = %v, want a refusal", err)
	}
	if err := delivery.Authorize(t.Context(), pkgchannel.SendControl); !errors.Is(err, errDeliveryUnauthorized) {
		t.Fatalf("control authorize without a record = %v, want a refusal", err)
	}
}

// A record for another channel instance or chat is not this attempt's authority,
// even on the same platform. The mismatch is refused, so a superseded or
// misrouted handle cannot send what another delivery owner owns.
func TestAuthorizeRequiresTheExactTarget(t *testing.T) {
	f := newDeliveryFixture(t)
	f.newDelivery(t)
	for name, target := range map[string]deliveryTarget{
		"other channel instance": {Platform: f.target.Platform, ChannelID: "telegram-other", ChatID: f.target.ChatID, ThreadID: f.target.ThreadID, ReplyTo: f.target.ReplyTo},
		"other chat":             {Platform: f.target.Platform, ChannelID: f.target.ChannelID, ChatID: "chat-2", ThreadID: f.target.ThreadID, ReplyTo: f.target.ReplyTo},
		"other thread":           {Platform: f.target.Platform, ChannelID: f.target.ChannelID, ChatID: f.target.ChatID, ThreadID: "topic-2", ReplyTo: f.target.ReplyTo},
		"other reply target":     {Platform: f.target.Platform, ChannelID: f.target.ChannelID, ChatID: f.target.ChatID, ThreadID: f.target.ThreadID, ReplyTo: "msg-2"},
		"other platform":         {Platform: pkgchannel.PlatformDiscord, ChannelID: f.target.ChannelID, ChatID: f.target.ChatID, ThreadID: f.target.ThreadID, ReplyTo: f.target.ReplyTo},
	} {
		t.Run(name, func(t *testing.T) {
			mismatched := &runDelivery{
				q: sqlc.New(f.db), lease: f.lease, runID: f.lease.Guard.RunID,
				target: target, done: make(chan struct{}),
			}
			if err := mismatched.Authorize(t.Context(), pkgchannel.SendOutput); !errors.Is(err, errDeliveryUnauthorized) {
				t.Fatalf("authorize from %s = %v, want a refusal", name, err)
			}
		})
	}
	// The mismatch is not merely local: the attempt marker of the real record is
	// untouched, because no send was authorized against it.
	if output := f.output(t); output.AttemptAt.Valid {
		t.Fatal("a mismatched authorize recorded an attempt against the real record")
	}
}

// A record already settled by its own delivery attempt stops authorizing sends.
func TestSettledDeliveryStopsAuthorizingSends(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCompleted, Reason: "turn completed",
		Output: agentruntime.TurnOutput{Text: "hello"},
	}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatalf("commit handoff: %v", err)
	}
	if err := delivery.Settle(t.Context(), pkgchannel.DeliverySent); err != nil {
		t.Fatalf("settle: %v", err)
	}
	select {
	case <-delivery.Done():
	default:
		t.Fatal("settling the delivery did not release the per-chat FIFO")
	}
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); !errors.Is(err, errDeliverySettled) {
		t.Fatalf("authorize after settle = %v, want errDeliverySettled", err)
	}
	if output := f.output(t); output.State != "sent" || !output.SettledAt.Valid {
		t.Fatalf("output = state=%q settled=%v, want sent with a settle time", output.State, output.SettledAt.Valid)
	}
}

// A canceled turn's recorded output is not deliverable: the delivery record is
// settled conservatively rather than left as a replayable reply.
func TestRunRecoveryDoesNotSettleAnActiveChannelHandler(t *testing.T) {
	f := newDeliveryFixture(t)
	f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{
		Status: agentrun.StatusCanceled, Reason: "turn canceled",
		Output: agentruntime.TurnOutput{Text: "half a sen"},
	}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, false); err != nil {
		t.Fatalf("commit canceled handoff: %v", err)
	}
	run, err := sqlc.New(f.db).GetAgentRun(t.Context(), f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentrun.StatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	if output := f.output(t); output.State != "live" || output.Text != "" {
		t.Fatalf("output = state=%q text=%q, want a live record with no published reply", output.State, output.Text)
	}
	// Execution terminality says nothing about the channel handler, even later.
	if _, err := f.db.Exec(t.Context(), "UPDATE agent_run SET completed_at = clock_timestamp() - interval '1 minute' WHERE id = $1", f.lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reap(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if output := f.output(t); output.State != "live" {
		t.Fatalf("run recovery changed active delivery state to %q", output.State)
	}
}

// An attempt that may have reached the platform is recovered as unknown, never as
// an unsent reply a later owner could replay.
func TestUnsettledAttemptRemainsUncertainAfterExecutionRecovery(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatalf("authorize live delta: %v", err)
	}
	// The process dies before committing: only recovery remains.
	if _, err := f.db.Exec(t.Context(), `UPDATE agent_run
		SET status = 'interrupted', terminal_reason = 'lease_expired',
		    completed_at = clock_timestamp() - interval '1 minute',
		    lease_expires_at = clock_timestamp() WHERE id = $1`, f.lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reap(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if output := f.output(t); output.State != "live" || !output.AttemptAt.Valid {
		t.Fatalf("execution recovery lost the unsettled attempt: state=%q attempted=%v", output.State, output.AttemptAt.Valid)
	}
}

// deliveryRecordFixture keeps the fixture honest about the record's routing.
func TestDeliveryRecordCapturesRouting(t *testing.T) {
	f := newDeliveryFixture(t)
	f.newDelivery(t)
	output := f.output(t)
	if output.Platform != f.target.Platform || output.ChannelID != f.target.ChannelID ||
		output.ChatID != f.target.ChatID || output.ThreadID != f.target.ThreadID || output.ReplyTo != f.target.ReplyTo {
		t.Fatalf("routing = %+v, want %+v", output, f.target)
	}
	if output.SessionID != f.lease.Guard.SessionID {
		t.Fatalf("session = %q, want %q", output.SessionID, f.lease.Guard.SessionID)
	}
}

func TestDeliveryFailureDoesNotChangeSuccessfulExecution(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Settle(t.Context(), pkgchannel.DeliveryUnknown); err != nil {
		t.Fatal(err)
	}
	outcome := agentruntime.TurnOutcome{Status: agentrun.StatusCompleted, Output: agentruntime.TurnOutput{Text: "finished after the channel failed"}}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatal(err)
	}
	run, err := sqlc.New(f.db).GetAgentRun(t.Context(), f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	output := f.output(t)
	if run.Status != agentrun.StatusCompleted || output.State != "unknown" || output.Text != outcome.Output.Text {
		t.Fatalf("execution=%s delivery=%s text=%q", run.Status, output.State, output.Text)
	}
}

func TestAttemptedDeliveryCannotBeRecordedAsNotSent(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	if err := delivery.Authorize(t.Context(), pkgchannel.SendControl); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Settle(t.Context(), pkgchannel.DeliveryNotSent); err != nil {
		t.Fatal(err)
	}
	if output := f.output(t); output.State != "unknown" {
		t.Fatalf("attempted delivery became %s", output.State)
	}
}

// The query has observed 'live', but Finish commits before Check sees the Run.
func TestSendAuthorizationAcrossExecutionCompletion(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{Status: agentrun.StatusCompleted, Output: agentruntime.TurnOutput{Text: "committed"}}
	delivery.q = sqlc.New(finishBetweenQueries{DBTX: f.db, finish: func() {
		if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
			t.Fatal(err)
		}
	}})
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatalf("completion raced authorization: %v", err)
	}
}

type finishBetweenQueries struct {
	sqlc.DBTX
	finish func()
}

func (db finishBetweenQueries) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := db.DBTX.QueryRow(ctx, query, args...)
	if strings.Contains(query, "AuthorizeAgentRunOutputSend") {
		return finishAfterScan{Row: row, finish: db.finish}
	}
	return row
}

type finishAfterScan struct {
	pgx.Row
	finish func()
}

func (row finishAfterScan) Scan(dest ...any) error {
	if err := row.Row.Scan(dest...); err != nil {
		return err
	}
	row.finish()
	return nil
}

func TestCommittedTimeoutNoticeOutlivesFailedExecution(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	outcome := agentruntime.TurnOutcome{Status: agentrun.StatusFailed, Reason: "timeout", Output: agentruntime.TurnOutput{Text: "time limit reached"}}
	if !deliverableTurn(outcome, f.target, delivery) {
		t.Fatal("committed timeout notice cannot be handed to its channel")
	}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, true); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatalf("committed timeout notice lost its authority: %v", err)
	}
}
