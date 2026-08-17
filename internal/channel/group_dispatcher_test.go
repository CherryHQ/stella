package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingGroupPublisher struct {
	err   error
	calls int
	texts []string
}

type recordingPublisherReconstructor struct {
	publisher GroupPublisher
	channel   config.Channel
	envelope  GroupOutboxEnvelope
	calls     int
}

func (r *recordingPublisherReconstructor) ReconstructGroupPublisher(_ context.Context, configured config.Channel, envelope GroupOutboxEnvelope) (GroupPublisher, error) {
	r.calls++
	r.channel = configured
	r.envelope = envelope
	return r.publisher, nil
}

type blockingGroupPublisher struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if req.Stream != nil {
		for range req.Stream.Events {
		}
	}
	return nil
}

func (p *recordingGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
	p.calls++
	if req.Stream != nil {
		for evt := range req.Stream.Events {
			if evt.Text != "" {
				p.texts = append(p.texts, evt.Text)
			}
		}
	}
	return p.err
}

type dispatcherFixture struct {
	db      *pgxpool.Pool
	q       *sqlc.Queries
	d       *GroupDispatcher
	outbox  sqlc.CtxGroupOutbox
	message sqlc.CtxGroupMessage
	groupID string
}

type groupAttachmentAdmissionFixture struct {
	db      *pgxpool.Pool
	q       *sqlc.Queries
	coord   *Coordinator
	message pkgchannel.IncomingMessage
	groupID string
	agentID string
	channel string
}

func newGroupAttachmentAdmissionFixture(t *testing.T) groupAttachmentAdmissionFixture {
	t.Helper()
	ts := setupStores(t)
	ctx := t.Context()
	q := sqlc.New(ts.db)
	user := createTestUser(t, ts.oidcStore, "group-attachment@example.test")
	createTestIdentity(t, ts.oidcStore, user.ID, "web", "attachment-sender", "Attachment Sender")
	agentID := "attachment-agent"
	channelID := "attachment-channel"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: agentID, Name: "Attachment Agent", Workspace: t.TempDir(),
		Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create attachment agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID: channelID, AgentID: pgtype.Text{String: agentID, Valid: true},
	}); err != nil {
		t.Fatalf("create attachment channel: %v", err)
	}
	state, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID: uuid.NewString(), Platform: "web", PlatformGroupID: "attachment-group",
		CreatedByUserID: pgtype.Text{String: user.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("create attachment group: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID: state.ID, AgentID: agentID, ReplyChannelID: channelID,
	}); err != nil {
		t.Fatalf("add attachment group member: %v", err)
	}
	media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		UserID: user.ID, Sha256: make([]byte, 32), MimeType: "image/png", SizeBytes: 321,
	})
	if err != nil {
		t.Fatalf("create attachment media: %v", err)
	}
	coord := &Coordinator{
		store: ts.store, auth: ts.oidcStore, db: ts.db,
		eventLog: eventlog.NewStore(ts.db),
		arbiter:  NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3}),
	}
	dispatcher := NewGroupDispatcher(ts.db, coord, NewPublisherRegistry())
	coord.SetGroupDispatcher(dispatcher)
	return groupAttachmentAdmissionFixture{
		db: ts.db, q: q, coord: coord, groupID: state.ID, agentID: agentID, channel: channelID,
		message: pkgchannel.IncomingMessage{
			Platform: "web", ChannelID: channelID, SenderID: "attachment-sender",
			ChatID: "attachment-group", MessageID: "attachment-delivery-1", IsGroup: true,
			Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: media.ID}},
		},
	}
}

func TestGroupAttachmentAdmissionAtomicallyMaterializesEventRouteAndFIFO(t *testing.T) {
	fx := newGroupAttachmentAdmissionFixture(t)
	if err := fx.coord.AdmitAttachments(t.Context(), fx.message); err != nil {
		t.Fatalf("AdmitAttachments: %v", err)
	}
	var messages, outboxes, dispatches, fifos int
	var routeStatus, dispatchStatus string
	if err := fx.db.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM ctx_group_message WHERE group_id = $1::uuid),
		  (SELECT count(*) FROM ctx_group_outbox WHERE group_id = $1::uuid),
		  (SELECT count(*) FROM ctx_group_dispatch WHERE group_id = $1::uuid),
		  (SELECT count(*) FROM channel_binding_fifo WHERE principal_id = $1::text),
		  (SELECT status FROM channel_group_route WHERE group_id = $1::uuid),
		  (SELECT status FROM ctx_group_dispatch WHERE group_id = $1::uuid)
	`, fx.groupID).Scan(&messages, &outboxes, &dispatches, &fifos, &routeStatus, &dispatchStatus); err != nil {
		t.Fatalf("read atomic group admission: %v", err)
	}
	if messages != 1 || outboxes != 1 || dispatches != 1 || fifos != 1 || routeStatus != "completed" || dispatchStatus != "pending" {
		t.Fatalf("atomic group admission = messages:%d outboxes:%d dispatches:%d fifos:%d route:%s dispatch:%s", messages, outboxes, dispatches, fifos, routeStatus, dispatchStatus)
	}
	// The ordinary asynchronous outbox pass sees the completed route and must
	// not rematerialize its dispatch/FIFO. Keep execution out of scope by making
	// the already-admitted dispatch not yet due.
	if _, err := fx.db.Exec(t.Context(), `UPDATE ctx_group_dispatch SET next_attempt_at = now() + interval '1 hour' WHERE group_id = $1`, fx.groupID); err != nil {
		t.Fatalf("defer group execution: %v", err)
	}
	var messageID string
	if err := fx.db.QueryRow(t.Context(), `SELECT id FROM ctx_group_message WHERE group_id = $1`, fx.groupID).Scan(&messageID); err != nil {
		t.Fatalf("read group message ID: %v", err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(t.Context(), messageID)
	if err != nil {
		t.Fatalf("read admitted outbox: %v", err)
	}
	if err := fx.coord.groupDispatcher.ProcessOutbox(t.Context(), outbox); err != nil {
		t.Fatalf("process already-materialized outbox: %v", err)
	}
	if err := fx.db.QueryRow(t.Context(), `
		SELECT count(*), (SELECT status FROM ctx_group_outbox WHERE id = $2)
		FROM channel_binding_fifo WHERE principal_id = $1
	`, fx.groupID, outbox.ID).Scan(&fifos, &dispatchStatus); err != nil || fifos != 1 || dispatchStatus != "completed" {
		t.Fatalf("async pass = FIFO rows:%d outbox:%s err=%v, want 1/completed", fifos, dispatchStatus, err)
	}
	// Stable redelivery observes the event receipt and creates no second FIFO.
	if err := fx.coord.AdmitAttachments(t.Context(), fx.message); err != nil {
		t.Fatalf("redeliver attachment: %v", err)
	}
	if err := fx.db.QueryRow(t.Context(), `SELECT count(*) FROM channel_binding_fifo WHERE principal_id = $1::text`, fx.groupID).Scan(&fifos); err != nil || fifos != 1 {
		t.Fatalf("redelivery FIFO rows = %d err=%v, want 1", fifos, err)
	}
}

func TestGroupAttachmentAdmissionRollsBackEveryArtifactOnQuotaRejection(t *testing.T) {
	fx := newGroupAttachmentAdmissionFixture(t)
	if _, err := fx.db.Exec(t.Context(), `
		INSERT INTO channel_binding_fifo (
			id, channel_id, binding_key, principal_id, source_key, kind,
			payload, immutable_media, payload_bytes, attachment_bytes, binding_revision
		)
		SELECT gen_random_uuid(), $1, $2, $3, 'quota-seed-' || n::text,
		       'message', '[]'::jsonb, '[]'::jsonb,
		       pg_column_size('[]'::jsonb::text), 0, n
		FROM generate_series(1, 128) AS n
	`, fx.channel, agent.BuildGroupSessionKey(fx.agentID, fx.groupID), fx.groupID); err != nil {
		t.Fatalf("seed binding row quota: %v", err)
	}
	if err := fx.coord.AdmitAttachments(t.Context(), fx.message); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("AdmitAttachments error = %v, want quota rejection", err)
	}
	var artifacts, fifoRows int
	if err := fx.db.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM ctx_group_message WHERE group_id = $1::uuid) +
		  (SELECT count(*) FROM ctx_group_outbox WHERE group_id = $1::uuid) +
		  (SELECT count(*) FROM channel_group_route WHERE group_id = $1::uuid) +
		  (SELECT count(*) FROM ctx_group_dispatch WHERE group_id = $1::uuid),
		  (SELECT count(*) FROM channel_binding_fifo WHERE principal_id = $1::text)
	`, fx.groupID).Scan(&artifacts, &fifoRows); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 {
		t.Fatalf("failed attachment admission left %d durable artifacts, want zero", artifacts)
	}
	if fifoRows != 128 {
		t.Fatalf("failed attachment admission changed FIFO rows to %d, want 128 seeds", fifoRows)
	}
}

func newDispatcherFixture(t *testing.T, platform, envelope string) dispatcherFixture {
	t.Helper()
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:        "agent-1",
		Name:      "Agent One",
		Workspace: t.TempDir(),
		Sandbox:   json.RawMessage("{}"),
		Scope:     "system",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      "ch-1",
		AgentID: pgtype.Text{String: "agent-1", Valid: true},
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	state, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               "11111111-1111-1111-1111-111111111111",
		Platform:         platform,
		PlatformGroupID:  "physical-group-1",
		PlatformThreadID: "",
		GroupName:        "Group One",
		CreatedByUserID:  pgtype.Text{},
	})
	if err != nil {
		t.Fatalf("create group state: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        state.ID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
	}); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	message, err := q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{
		ID:        "a1a1a1a1-0000-0000-0000-000000000001",
		GroupID:   state.ID,
		Seq:       1,
		ActorType: string(eventlog.ActorHuman),
		ActorID:   "user-1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("create group message: %v", err)
	}
	setGroupNextSeq(t, db, state.ID, message.Seq)
	outbox, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
		ID:             "b0b0b0b0-0000-0000-0000-000000000001",
		GroupMessageID: message.ID,
		GroupID:        state.ID,
		Envelope:       envelope,
		Status:         "pending",
		LastError:      "",
	})
	if err != nil {
		t.Fatalf("create outbox: %v", err)
	}
	coord := &Coordinator{store: cfgstore.NewDBStore(db), arbiter: NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3})}
	d := NewGroupDispatcher(db, coord, NewPublisherRegistry())
	d.leaseDuration = 0
	d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		return textStream("ok"), nil
	}
	return dispatcherFixture{db: db, q: q, d: d, outbox: outbox, message: message, groupID: state.ID}
}

func textStream(text string) *pkgchannel.ChatStream {
	ch := make(chan pkgchannel.Event, 1)
	ch <- pkgchannel.Event{Text: text}
	close(ch)
	return &pkgchannel.ChatStream{Events: ch, SessionID: "session-1"}
}

func TestGroupRouteStaleClaimCannotMaterializeResponders(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := t.Context()
	route, err := fx.q.CreateChannelGroupRoute(ctx, sqlc.CreateChannelGroupRouteParams{
		ID: "c0c0c0c0-0000-0000-0000-000000000001", GroupMessageID: fx.message.ID,
		GroupID: fx.groupID, GroupSeq: fx.message.Seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := fx.q.ClaimChannelGroupRoute(ctx, sqlc.ClaimChannelGroupRouteParams{
		ClaimToken: pgtype.Text{String: "d0d0d0d0-0000-0000-0000-000000000001", Valid: true}, LeaseSeconds: 1, ID: route.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE channel_group_route SET claim_expires_at = now() - interval '1 second' WHERE id = $1`, route.ID); err != nil {
		t.Fatal(err)
	}
	second, err := fx.q.ClaimChannelGroupRoute(ctx, sqlc.ClaimChannelGroupRouteParams{
		ClaimToken: pgtype.Text{String: "d0d0d0d0-0000-0000-0000-000000000002", Valid: true}, LeaseSeconds: 30, ID: route.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qtx := fx.q.WithTx(tx)
	if err := qtx.CreateGroupDispatch(ctx, sqlc.CreateGroupDispatchParams{
		ID: "e0e0e0e0-0000-0000-0000-000000000001", GroupMessageID: fx.message.ID,
		GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1", Status: "pending", LastError: "",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := qtx.CompleteChannelGroupRoute(ctx, sqlc.CompleteChannelGroupRouteParams{
		Decisions: json.RawMessage(`["agent-1"]`), ID: route.ID, ClaimToken: first.ClaimToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("stale claimant completed %d GroupRoutes", rows)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := fx.q.CountGroupDispatchByMessage(ctx, fx.message.ID); err != nil || count != 0 {
		t.Fatalf("stale responder materialization count=%d err=%v", count, err)
	}
	if second.ClaimToken.String == first.ClaimToken.String {
		t.Fatal("replacement claim did not receive a new token")
	}
}

func TestGroupRouteTerminalClassificationFailureIsAuditedAndReleasesOrdering(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := t.Context()
	route, err := fx.q.CreateChannelGroupRoute(ctx, sqlc.CreateChannelGroupRouteParams{
		ID: "c1c1c1c1-0000-0000-0000-000000000001", GroupMessageID: fx.message.ID,
		GroupID: fx.groupID, GroupSeq: fx.message.Seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.q.ClaimChannelGroupRoute(ctx, sqlc.ClaimChannelGroupRouteParams{
		ClaimToken: pgtype.Text{String: "d1d1d1d1-0000-0000-0000-000000000001", Valid: true}, LeaseSeconds: 30, ID: route.ID,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := fx.q.ClaimPendingGroupOutbox(ctx, sqlc.ClaimPendingGroupOutboxParams{
		ID: fx.outbox.ID, Now: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		LeaseUntil: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.failOutboxTerminal(ctx, claimed, "classification_invalid"); err != nil {
		t.Fatal(err)
	}
	failed, err := fx.q.GetGroupOutbox(ctx, fx.outbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := fx.q.GetChannelGroupRouteByMessage(ctx, fx.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.LastError != "classification_invalid" {
		t.Fatalf("terminal outbox = status %q reason %q", failed.Status, failed.LastError)
	}
	if completed.Status != "completed" || string(completed.Decisions) != "[]" || !completed.CompletedAt.Valid {
		t.Fatalf("terminal GroupRoute = status %q decisions %s completed=%v", completed.Status, completed.Decisions, completed.CompletedAt.Valid)
	}

	nextMessage := createGroupMessageWithSeq(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000002", fx.message.Seq+1)
	nextRoute, err := fx.q.CreateChannelGroupRoute(ctx, sqlc.CreateChannelGroupRouteParams{
		ID: "c1c1c1c1-0000-0000-0000-000000000002", GroupMessageID: nextMessage.ID,
		GroupID: fx.groupID, GroupSeq: nextMessage.Seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.q.ClaimChannelGroupRoute(ctx, sqlc.ClaimChannelGroupRouteParams{
		ClaimToken: pgtype.Text{String: "d1d1d1d1-0000-0000-0000-000000000002", Valid: true}, LeaseSeconds: 30, ID: nextRoute.ID,
	}); err != nil {
		t.Fatalf("successor GroupRoute remained blocked after audited rejection: %v", err)
	}
}

func TestChannelBindingFIFODedupPoisonHeadAndAuditedRejection(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := t.Context()
	create := func(id, source string) sqlc.ChannelBindingFifo {
		row, err := fx.q.CreateChannelBindingFIFO(ctx, sqlc.CreateChannelBindingFIFOParams{
			ID: id, ChannelID: "ch-1", BindingKey: "binding-1", SourceKey: source,
			Kind: "message", Payload: json.RawMessage(`[{"kind":"text","text":"hello"}]`), ImmutableMedia: json.RawMessage(`[]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	first := create("f0f0f0f0-0000-0000-0000-000000000001", "source-1")
	if duplicate := create("f0f0f0f0-0000-0000-0000-000000000099", "source-1"); duplicate.ID != first.ID {
		t.Fatalf("stable source created duplicate FIFO row %s", duplicate.ID)
	}
	second := create("f0f0f0f0-0000-0000-0000-000000000002", "source-2")
	if second.BindingRevision != first.BindingRevision+1 {
		t.Fatalf("binding revisions = %d, %d; want contiguous order", first.BindingRevision, second.BindingRevision)
	}
	if _, err := fx.q.CreateChannelBindingFIFO(ctx, sqlc.CreateChannelBindingFIFOParams{
		ID: "f0f0f0f0-0000-0000-0000-000000000098", ChannelID: "ch-1", BindingKey: "binding-1",
		SourceKey: "source-1", Kind: "message", Payload: json.RawMessage(`[{"kind":"text","text":"changed"}]`),
		ImmutableMedia: json.RawMessage(`[]`),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("source identity accepted changed immutable input: %v", err)
	}
	if _, err := fx.q.ClaimChannelBindingFIFOHead(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE channel_binding_fifo SET claim_expires_at = now() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := fx.q.ClaimChannelBindingFIFOHead(ctx, first.ID)
	if err != nil || reclaimed.AttemptCount != 2 {
		t.Fatalf("expired unlinked claim was not recoverable: attempts=%d err=%v", reclaimed.AttemptCount, err)
	}
	if _, err := fx.q.ClaimChannelBindingFIFOHead(ctx, second.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor overtook running head: %v", err)
	}
	if _, err := fx.q.BlockChannelBindingFIFO(ctx, sqlc.BlockChannelBindingFIFOParams{
		Reason: "poison", BackoffSeconds: 60, ID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.q.ClaimChannelBindingFIFOHead(ctx, second.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor overtook blocked poison head: %v", err)
	}
	// Exhaust the bounded automatic retry budget. The head must remain blocked
	// and observable instead of being silently skipped or retried forever.
	if _, err := fx.db.Exec(ctx, `UPDATE channel_binding_fifo SET attempt_count = 5, next_attempt_at = now() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := fx.q.RetryBlockedChannelBindingFIFO(ctx, first.ID); err != nil || rows != 0 {
		t.Fatalf("exhausted poison head automatic retry rows=%d err=%v", rows, err)
	}
	blocked, err := fx.q.GetChannelBindingFIFO(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" || blocked.BlockedReason != "poison" {
		t.Fatalf("exhausted poison head = status %q reason %q", blocked.Status, blocked.BlockedReason)
	}
	if _, err := fx.q.ClaimChannelBindingFIFOHead(ctx, second.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor overtook exhausted poison head: %v", err)
	}
	if rows, err := fx.q.RejectChannelBindingFIFO(ctx, sqlc.RejectChannelBindingFIFOParams{
		Reason: "operator_rejected", RejectedBy: "admin-1", ID: first.ID,
	}); err != nil || rows != 1 {
		t.Fatalf("audited rejection rows=%d err=%v", rows, err)
	}
	if _, err := fx.q.ClaimChannelBindingFIFOHead(ctx, second.ID); err != nil {
		t.Fatalf("successor did not proceed after explicit rejection: %v", err)
	}
}

func createDispatchForGroupMessage(t *testing.T, q *sqlc.Queries, msg sqlc.CtxGroupMessage, id, agentID, groupID string, status string, leaseUntil pgtype.Timestamptz) {
	t.Helper()
	if err := q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             id,
		GroupMessageID: msg.ID,
		GroupID:        groupID,
		AgentID:        agentID,
		ReplyChannelID: "ch-1",
		Status:         status,
		LeaseUntil:     leaseUntil,
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch %s: %v", id, err)
	}
}

func createGroupMessageWithSeq(t *testing.T, q *sqlc.Queries, groupID, id string, seq int64) sqlc.CtxGroupMessage {
	t.Helper()
	msg, err := q.CreateGroupMessage(context.Background(), sqlc.CreateGroupMessageParams{
		ID:        id,
		GroupID:   groupID,
		Seq:       seq,
		ActorType: string(eventlog.ActorHuman),
		ActorID:   "user-1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("create message %s: %v", id, err)
	}
	return msg
}

func listPendingDispatchIDs(t *testing.T, q *sqlc.Queries, now time.Time, limit int32) []string {
	t.Helper()
	rows, err := q.ListPendingGroupDispatch(context.Background(), sqlc.ListPendingGroupDispatchParams{
		Now:        nullTime(now),
		LimitCount: limit,
	})
	if err != nil {
		t.Fatalf("list pending dispatch: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestListPendingGroupDispatchBlocksLaterSameAgentSeq(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	earlier := fx.message
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, earlier, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000001") || containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want earlier only", ids)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET status = 'completed' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier: %v", err)
	}
	ids = listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later after terminal earlier", ids)
	}
}

func TestListPendingGroupDispatchExpiredRunningDoesNotBlockLater(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "running", nullTime(now.Add(-time.Minute)))
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want expired running not to block later", ids)
	}
}

func TestListPendingGroupDispatchBlocksLaterWhenEarlierOutboxNotTerminal(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	now := time.Now().UTC()
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later blocked by earlier outbox", ids)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier outbox: %v", err)
	}
	ids = listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later after terminal earlier outbox", ids)
	}
}

func TestListPendingGroupDispatchBlockedRowsDoNotConsumeLimit(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	for i := range 30 {
		msg := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, fmt.Sprintf("a1a1a1a1-0000-0000-0000-00000000b%03d", i), int64(i+2))
		createDispatchForGroupMessage(t, fx.q, msg, fmt.Sprintf("d15a0000-0000-0000-0000-00000000b%03d", i), "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	}
	otherGroup, err := fx.q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{ID: "22222222-2222-2222-2222-222222222222", Platform: "web", PlatformGroupID: "physical-group-2", GroupName: "Group Two"})
	if err != nil {
		t.Fatalf("create group-2: %v", err)
	}
	otherMsg := createGroupMessageWithSeq(t, fx.q, otherGroup.ID, "a1a1a1a1-0000-0000-0000-00000000000f", 1)
	createDispatchForGroupMessage(t, fx.q, otherMsg, "d15a0000-0000-0000-0000-0000000000fe", "agent-1", otherGroup.ID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-0000000000fe") {
		t.Fatalf("pending ids = %v, want other group despite 30 blocked rows", ids)
	}
}

func TestListPendingGroupDispatchGateIsPerGroupAgent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	if _, err := fx.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "agent-2", Name: "Agent Two", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("create agent-2: %v", err)
	}
	state2, err := fx.q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: "22222222-2222-2222-2222-222222222222", Platform: "web", PlatformGroupID: "physical-group-2", GroupName: "Group Two"})
	if err != nil {
		t.Fatalf("create group-2: %v", err)
	}
	laterSameAgent := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	laterOtherAgent := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000003", 3)
	otherGroup := createGroupMessageWithSeq(t, fx.q, state2.ID, "a1a1a1a1-0000-0000-0000-000000000004", 2)
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, laterSameAgent, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, laterOtherAgent, "d15a0000-0000-0000-0000-000000000003", "agent-2", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, otherGroup, "d15a0000-0000-0000-0000-000000000004", "agent-1", state2.ID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if containsString(ids, "d15a0000-0000-0000-0000-000000000002") || !containsString(ids, "d15a0000-0000-0000-0000-000000000003") || !containsString(ids, "d15a0000-0000-0000-0000-000000000004") {
		t.Fatalf("pending ids = %v, want same-agent blocked only", ids)
	}
}

func containsString(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

func TestGroupDispatcherReapExpiredCompletesDispatchWithResultMarker(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	past := time.Now().UTC().Add(-time.Minute)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-0000000000ff",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.message.GroupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   fx.d.maxAttempts,
		LeaseUntil:     nullTime(past),
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-0000000000ff'`); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	if err := fx.d.reapExpired(context.Background()); err != nil {
		t.Fatalf("reap expired: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-0000000000ff")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if dispatch.Status != "completed" {
		t.Fatalf("dispatch status = %q, want completed", dispatch.Status)
	}
}

func TestGroupDispatcherProcessOutboxHappyPath(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
	if publisher.calls != 1 || len(publisher.texts) != 1 || publisher.texts[0] != "ok" {
		t.Fatalf("publisher calls/texts = %d/%v, want one ok", publisher.calls, publisher.texts)
	}
	status := dispatchStatusByMessage(t, fx.db, fx.message.ID)
	if status != "completed" {
		t.Fatalf("dispatch status = %q, want completed", status)
	}
}

func TestGroupDispatcherWebNoMentionSingleMemberFallbackCreatesOneDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

func TestGroupDispatcherWebNoMentionMultiMemberStaysSilent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	addSecondMember(t, fx)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0", count)
	}
}

// A multi-member platform (non-web) group with no semantic arbiter takes the
// degraded Warn path and stays silent — it must never broadcast to every member
// the way the deleted `group_mode: always` fallback once did. This is the
// positive lock on that branch: it fails if any all-members fallback is
// reintroduced for platform groups.
func TestGroupDispatcherPlatformNoMentionMultiMemberNoArbiterStaysSilent(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	addSecondMember(t, fx)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0 (platform multi-member, no arbiter)", count)
	}
}

func TestGroupDispatcherZeroRespondersCompletesOutbox(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0", count)
	}
}

func TestGroupDispatcherResolvesEnvelopeMentionAtDispatch(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{Raw: "@bot1", PlatformID: "bot1"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	reg := NewBotIdentityRegistry()
	reg.Register("telegram", "bot1", "ch-1")
	fx.d.coord.botRegistry = reg
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

func TestGroupDispatcherDispatchesAllMentionedMembers(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}, {AgentID: "agent-2"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	addSecondMember(t, fx)
	firstPublisher := &recordingGroupPublisher{}
	secondPublisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", firstPublisher)
	fx.d.publishers.Register("ch-2", secondPublisher)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 2 || got[0] != "agent-1" || got[1] != "agent-2" {
		t.Fatalf("dispatch agents = %v, want [agent-1 agent-2]", got)
	}
	if firstPublisher.calls != 1 || secondPublisher.calls != 1 {
		t.Fatalf("publisher calls = %d/%d, want 1/1", firstPublisher.calls, secondPublisher.calls)
	}
}

func TestGroupRoutePartialBusyFanOutRecordsRejectedAndAcceptedResponders(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}, {AgentID: "agent-2"}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	addSecondMember(t, fx)
	payload := json.RawMessage(`[{"kind":"text","text":"already queued"}]`)
	for i := range 128 {
		_, err := fx.q.CreateChannelBindingFIFO(t.Context(), sqlc.CreateChannelBindingFIFOParams{
			ID: uuid.Must(uuid.NewV7()).String(), ChannelID: "ch-1",
			BindingKey:  agent.BuildGroupSessionKey("agent-1", fx.groupID),
			PrincipalID: fx.groupID, SourceKey: fmt.Sprintf("busy-%d", i),
			Kind: "message", Payload: payload, ImmutableMedia: json.RawMessage(`[]`),
		})
		if err != nil {
			t.Fatalf("seed busy responder %d: %v", i, err)
		}
	}
	fx.d.publishers.Register("ch-2", &recordingGroupPublisher{})
	if err := fx.d.ProcessOutbox(t.Context(), fx.outbox); err != nil {
		t.Fatalf("process partial-busy outbox: %v", err)
	}

	rows, err := fx.db.Query(t.Context(), `
		SELECT agent_id, status, last_error
		FROM ctx_group_dispatch
		WHERE group_message_id = $1
	`, fx.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type dispatchStatus struct{ status, lastError string }
	statuses := make(map[string]dispatchStatus, 2)
	for rows.Next() {
		var agentID string
		var row dispatchStatus
		if err := rows.Scan(&agentID, &row.status, &row.lastError); err != nil {
			t.Fatal(err)
		}
		statuses[agentID] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if row := statuses["agent-1"]; row.status != "failed" || !strings.Contains(row.lastError, "admission_rejected") {
		t.Fatalf("busy responder dispatch = status %q error %q, want explicit admission rejection", row.status, row.lastError)
	}
	if row := statuses["agent-2"]; row.status != "completed" {
		t.Fatalf("accepted responder dispatch status = %q, want completed", row.status)
	}
	var acceptedFIFO, rejectedFIFO int
	if err := fx.db.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE source_responder_agent_id = 'agent-2'),
		       count(*) FILTER (WHERE source_responder_agent_id = 'agent-1')
		FROM channel_binding_fifo
		WHERE source_dispatch_id IS NOT NULL
	`).Scan(&acceptedFIFO, &rejectedFIFO); err != nil {
		t.Fatal(err)
	}
	if acceptedFIFO != 1 || rejectedFIFO != 0 {
		t.Fatalf("materialized responder FIFO rows = accepted %d rejected %d, want 1/0", acceptedFIFO, rejectedFIFO)
	}
	route, err := fx.q.GetChannelGroupRouteByMessage(t.Context(), fx.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Status != "completed" || !equalFIFOJSON(route.Decisions, json.RawMessage(`["agent-1","agent-2"]`)) {
		t.Fatalf("GroupRoute = status %q decisions %s, want completed with both classified responders", route.Status, route.Decisions)
	}
}

func TestGroupDispatchMaterializationPreservesImmutableImageRefs(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := t.Context()
	userID := uuid.NewString()
	if _, err := appdb.NewOIDCStore(fx.db).CreateUser(ctx, auth.User{
		ID: userID, Email: userID + "@example.test", Name: "Media Owner",
	}); err != nil {
		t.Fatalf("create media owner: %v", err)
	}
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	media, err := fx.q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		UserID: userID, Sha256: digest, MimeType: "image/png", SizeBytes: 321,
	})
	if err != nil {
		t.Fatalf("create immutable media: %v", err)
	}
	payload, err := ai.MarshalContentBlocks([]ai.ContentBlock{
		ai.TextContent{Text: "inspect"},
		ai.ImageRefContent{MediaID: media.ID, Baseline: ai.ImageBaseline{Text: "## Text\nlabel\n\n## Scene\na chart"}},
	})
	if err != nil {
		t.Fatalf("marshal canonical group input: %v", err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_message SET content_blocks = $1::jsonb WHERE id = $2`, payload, fx.message.ID); err != nil {
		t.Fatalf("store canonical group input: %v", err)
	}
	if err := fx.d.createDispatchRows(ctx, fx.q, fx.outbox, []string{"agent-1"}, false); err != nil {
		t.Fatalf("materialize group dispatch: %v", err)
	}

	var storedPayload, storedMedia json.RawMessage
	var storedBytes int64
	if err := fx.db.QueryRow(ctx, `
		SELECT payload, immutable_media, attachment_bytes
		FROM channel_binding_fifo
		WHERE source_responder_agent_id = 'agent-1'
	`).Scan(&storedPayload, &storedMedia, &storedBytes); err != nil {
		t.Fatalf("load group FIFO envelope: %v", err)
	}
	blocks, err := ai.UnmarshalContentBlocks(storedPayload)
	if err != nil {
		t.Fatalf("decode group FIFO payload: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("group FIFO blocks = %#v", blocks)
	}
	ref, ok := blocks[1].(ai.ImageRefContent)
	if !ok || ref.MediaID != media.ID || ref.Baseline.Text != "## Text\nlabel\n\n## Scene\na chart" {
		t.Fatalf("group FIFO image ref = %#v", blocks[1])
	}
	wantMedia := json.RawMessage(fmt.Sprintf(`[{"media_id":%q,"size_bytes":321}]`, media.ID))
	if !equalFIFOJSON(storedMedia, wantMedia) || storedBytes != 321 {
		t.Fatalf("group FIFO media = %s bytes=%d", storedMedia, storedBytes)
	}
	if durable := string(storedPayload) + string(storedMedia); strings.Contains(durable, "data:") || strings.Contains(durable, "https://") || strings.Contains(durable, "aGVsbG8=") {
		t.Fatalf("group FIFO retained expiring/provider-ready attachment data: %s", durable)
	}
}

func TestGroupDispatcherExistingDispatchSkipsEnvelopeDecode(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{not-json`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox should skip invalid envelope when dispatch rows exist: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
}

func TestGroupDispatcherPublishFailureIsTerminalAndNotRetried(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	boom := errors.New("boom")
	publisher := &recordingGroupPublisher{err: boom}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "failed" || dispatch.ResultMessageID != "" {
		t.Fatalf("dispatch status/result = %q/%q, want failed empty result", dispatch.Status, dispatch.ResultMessageID)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want 0", got)
	}

	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want exactly one ambiguous publish attempt", publisher.calls)
	}
}

func TestGroupDispatcherChatFailureIsTerminalAndNotRetried(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return nil, errors.New("stream setup failed after AgentRun admission")
	}
	const dispatchID = "d15a0000-0000-0000-0000-000000000091"
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             dispatchID,
		GroupMessageID: fx.message.ID,
		GroupID:        fx.groupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected chat-boundary error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != "failed" || !strings.Contains(dispatch.LastError, "outcome unknown") {
		t.Fatalf("dispatch = status %q error %q, want terminal outcome-unknown failure", dispatch.Status, dispatch.LastError)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("terminal redispatch: %v", err)
	}
	if chatCalls != 1 {
		t.Fatalf("chat calls = %d, want exactly one outcome-unknown attempt", chatCalls)
	}
}

func TestGroupDispatcherReconstructsPublisherWithoutLocalRegistration(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"lifecycle_feedback":true}`)
	publisher := &recordingGroupPublisher{}
	reconstructor := &recordingPublisherReconstructor{publisher: publisher}
	fx.d.reconstructor = reconstructor
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.groupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if _, registered := fx.d.publishers.Get("ch-1"); registered {
		t.Fatal("test requires no process-local publisher registration")
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute reconstructed dispatch: %v", err)
	}
	if reconstructor.calls != 1 || reconstructor.channel.ID != "ch-1" || !reconstructor.envelope.LifecycleFeedback {
		t.Fatalf("reconstruction calls/channel/envelope = %d/%q/%v", reconstructor.calls, reconstructor.channel.ID, reconstructor.envelope.LifecycleFeedback)
	}
	if publisher.calls != 1 || len(publisher.texts) != 1 || publisher.texts[0] != "ok" {
		t.Fatalf("publish calls/texts = %d/%v, want 1/[ok]", publisher.calls, publisher.texts)
	}
}

func TestGroupDispatcherWritebackFailureAfterPublishIsTerminal(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return textStream("ok"), nil
	}
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `CREATE FUNCTION fail_agent_writeback_fn() RETURNS trigger AS $$ BEGIN IF NEW.actor_type = 'agent' THEN RAISE EXCEPTION 'fail agent writeback'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `CREATE TRIGGER fail_agent_writeback BEFORE INSERT ON ctx_group_message FOR EACH ROW EXECUTE FUNCTION fail_agent_writeback_fn();`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected writeback error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "failed" || dispatch.ResultMessageID != "" {
		t.Fatalf("dispatch status/result = %q/%q, want failed empty result", dispatch.Status, dispatch.ResultMessageID)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want failed transaction to append none", got)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("terminal redispatch: %v", err)
	}
	if chatCalls != 1 || publisher.calls != 1 {
		t.Fatalf("chat/publish calls = %d/%d, want exactly one outcome-unknown attempt", chatCalls, publisher.calls)
	}
}

// TestGroupDispatcherSupersededTriggerCompletesWithoutChat pins the restart
// half of the group `/new` boundary: a dispatch row whose trigger seq sits at
// or below the agent's ingest cursor — a rotation consumed it while the row
// waited — retires as completed instead of running a pre-reset turn against
// the successor session.
func TestGroupDispatcherSupersededTriggerCompletesWithoutChat(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.chat = fx.d.chatDispatch // the cursor guard lives on the real chat path
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.UpsertIngestCursor(context.Background(), sqlc.UpsertIngestCursorParams{
		GroupID:  "11111111-1111-1111-1111-111111111111",
		Pipeline: memory.GroupIngestPipeline("agent-1"),
		LastSeq:  fx.message.Seq,
	}); err != nil {
		t.Fatalf("seed rotation boundary: %v", err)
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001",
		"agent-1", "11111111-1111-1111-1111-111111111111", "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("ExecuteDispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" {
		t.Fatalf("dispatch status = %q, want completed retirement", dispatch.Status)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want the superseded turn never published", publisher.calls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want the superseded turn never ran", got)
	}
}

func TestGroupDispatcherResultMessageSkipsChatPublishAndAppend(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return textStream("ok"), nil
	}
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   1,
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("set result marker: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" || chatCalls != 0 || publisher.calls != 0 {
		t.Fatalf("status/chat/publish = %q/%d/%d, want completed/0/0", dispatch.Status, chatCalls, publisher.calls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want 0", got)
	}
}

func TestGroupDispatcherWebWriteErrorStillRecordsResult(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.ResultMessageID == "" || dispatch.Status != "completed" {
		t.Fatalf("dispatch status/result = %q/%q, want completed result", dispatch.Status, dispatch.ResultMessageID)
	}
}

func TestGroupDispatcherPublisherFailureMarksFailedAtMaxAttempts(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.maxAttempts = 1
	boom := errors.New("boom")
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{err: boom})
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "failed" {
		t.Fatalf("dispatch status = %q, want failed", dispatch.Status)
	}
}

func TestGroupDispatcherDispatchSyncUsesPublisherOverride(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}

	if err := fx.d.DispatchSync(context.Background(), fx.outbox, publisher); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
}

func TestGroupDispatcherDispatchSyncWaitsForBlockedDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier outbox: %v", err)
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	setGroupNextSeq(t, fx.db, fx.message.GroupID, later.Seq)
	laterOutbox, err := fx.q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{ID: "b0b0b0b0-0000-0000-0000-000000000002", GroupMessageID: later.ID, GroupID: fx.message.GroupID, Envelope: "{}", Status: "pending", LastError: ""})
	if err != nil {
		t.Fatalf("create later outbox: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET status = 'completed' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`)
	}()
	publisher := &recordingGroupPublisher{}
	if err := fx.d.DispatchSync(ctx, laterOutbox, publisher); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
}

func TestGroupDispatcherExtendsOutboxLeaseWhileRunning(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	claimed, err := fx.q.ClaimPendingGroupOutbox(context.Background(), sqlc.ClaimPendingGroupOutboxParams{
		ID:         fx.outbox.ID,
		Now:        nullTime(time.Now().UTC()),
		LeaseUntil: nullTime(time.Now().UTC().Add(fx.d.leaseDuration)),
	})
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	initialLease := claimed.LeaseUntil.Time
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return fx.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, nil)
	defer stop()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("outbox lease was not extended; initial lease %q", initialLease)
		case <-ticker.C:
			current, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
			if err != nil {
				t.Fatalf("get outbox while waiting heartbeat: %v", err)
			}
			if !current.LeaseUntil.Time.IsZero() && !current.LeaseUntil.Time.Equal(initialLease) {
				return
			}
		}
	}
}

func TestGroupDispatcherHeartbeatDoesNotExtendAfterOwnershipLoss(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	claimed, err := fx.q.ClaimPendingGroupOutbox(context.Background(), sqlc.ClaimPendingGroupOutboxParams{
		ID:         fx.outbox.ID,
		Now:        nullTime(time.Now().UTC()),
		LeaseUntil: nullTime(time.Now().UTC().Add(fx.d.leaseDuration)),
	})
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return fx.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, nil)
	defer stop()
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET attempt_count = attempt_count + 1 WHERE id = $1`, claimed.ID); err != nil {
		t.Fatalf("simulate ownership loss: %v", err)
	}
	afterLoss, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("get outbox after ownership loss: %v", err)
	}
	time.Sleep(time.Second)
	current, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("get outbox after heartbeat: %v", err)
	}
	if !current.LeaseUntil.Time.Equal(afterLoss.LeaseUntil.Time) {
		t.Fatalf("stale heartbeat extended lease after ownership loss: before=%q after=%q", afterLoss.LeaseUntil.Time, current.LeaseUntil.Time)
	}
}

func TestGroupDispatcherCancelsDispatchAfterOwnershipLoss(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	publisher := &blockingGroupPublisher{started: make(chan struct{}), release: make(chan struct{})}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	errC := make(chan error, 1)
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch, nil) }()
	select {
	case <-publisher.started:
	case err := <-errC:
		t.Fatalf("execute dispatch returned before publisher started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET attempt_count = attempt_count + 1 WHERE id = $1`, "d15a0000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("simulate dispatch ownership loss: %v", err)
	}
	select {
	case err := <-errC:
		if err == nil {
			t.Fatal("execute dispatch succeeded after ownership loss")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute dispatch did not stop after ownership loss")
	}
	current, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after ownership loss: %v", err)
	}
	if current.Status == "completed" {
		t.Fatal("stale owner marked dispatch completed after ownership loss")
	}
}

func TestGroupDispatcherExtendsDispatchLeaseWhilePublishing(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	publisher := &blockingGroupPublisher{started: make(chan struct{}), release: make(chan struct{})}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	errC := make(chan error, 1)
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch, nil) }()
	select {
	case <-publisher.started:
	case err := <-errC:
		t.Fatalf("execute dispatch returned before publisher started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	running, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get running dispatch: %v", err)
	}
	initialLease := running.LeaseUntil.Time
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			close(publisher.release)
			t.Fatalf("dispatch lease was not extended; initial lease %q", initialLease)
		case <-ticker.C:
			current, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
			if err != nil {
				t.Fatalf("get dispatch while waiting heartbeat: %v", err)
			}
			if !current.LeaseUntil.Time.IsZero() && !current.LeaseUntil.Time.Equal(initialLease) {
				close(publisher.release)
				if err := <-errC; err != nil {
					t.Fatalf("execute dispatch after heartbeat: %v", err)
				}
				return
			}
		}
	}
}

func dispatchAgentsByMessage(t *testing.T, db *pgxpool.Pool, messageID string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT agent_id FROM ctx_group_dispatch WHERE group_message_id = $1 ORDER BY agent_id`, messageID)
	if err != nil {
		t.Fatalf("query dispatch agents: %v", err)
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			t.Fatalf("scan dispatch agent: %v", err)
		}
		agents = append(agents, agentID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dispatch agents: %v", err)
	}
	return agents
}

func dispatchStatusByMessage(t *testing.T, db *pgxpool.Pool, messageID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM ctx_group_dispatch WHERE group_message_id = $1`, messageID).Scan(&status); err != nil {
		t.Fatalf("query dispatch status: %v", err)
	}
	return status
}

func countAgentGroupMessages(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&count); err != nil {
		t.Fatalf("count agent messages: %v", err)
	}
	return count
}

func TestWrapGroupResponseStreamSkipsWritebackOnErrEvent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "partial"}
	stream <- pkgchannel.Event{Err: errors.New("boom")}
	close(stream)

	wrapped, responseC := fx.d.wrapGroupResponseStream(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	for range wrapped.Events {
	}
	response := <-responseC
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestWrapGroupResponseStreamSkipsWritebackAfterCancel(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	stream := make(chan pkgchannel.Event)
	wrapped, responseC := fx.d.wrapGroupResponseStream(ctx, &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})

	stream <- pkgchannel.Event{Text: "partial"}
	cancel()
	stream <- pkgchannel.Event{Text: " ignored"}
	close(stream)
	for range wrapped.Events {
	}
	response := <-responseC
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestWrapGroupResponseStreamBuffersCompleteStream(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "complete"}
	close(stream)

	wrapped, responseC := fx.d.wrapGroupResponseStream(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	for range wrapped.Events {
	}
	response := <-responseC
	if !response.complete || response.text != "complete" || response.sessionID != "session-1" {
		t.Fatalf("response = %+v, want complete buffered response", response)
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func setGroupNextSeq(t *testing.T, db *pgxpool.Pool, groupID string, seq int64) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `UPDATE ctx_group_state SET next_seq = $1 WHERE id = $2`, seq, groupID); err != nil {
		t.Fatalf("set group next seq: %v", err)
	}
}

type capturingGroupPublisher struct {
	req GroupPublishRequest
}

func (p *capturingGroupPublisher) Publish(_ context.Context, req GroupPublishRequest) error {
	p.req = req
	if req.Stream != nil {
		for range req.Stream.Events {
		}
	}
	return nil
}

// TestExecuteDispatchAbortClosureUsesSessionQueueGroupKey verifies the
// GroupPublishRequest.Abort closure ExecuteDispatch builds targets the exact
// same per-(agent,group) session queue slot chatDispatchUnqueued enqueues
// under. A wrong key would make a Discord Cancel click silently no-op
// instead of stopping the running turn.
func TestExecuteDispatchAbortClosureUsesSessionQueueGroupKey(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{"lifecycle_feedback":true}`)
	publisher := &capturingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)

	sessionKey := agent.BuildGroupSessionKey("agent-1", fx.groupID)
	wrongKey := agent.BuildGroupSessionKey("agent-2", fx.groupID)
	correctCalled := false
	wrongCalled := false

	slot := fx.d.queue.getOrCreate(sessionKey)
	slot.mu.Lock()
	slot.activeCancel = func() { correctCalled = true }
	slot.mu.Unlock()
	defer fx.d.queue.release(slot)

	wrongSlot := fx.d.queue.getOrCreate(wrongKey)
	wrongSlot.mu.Lock()
	wrongSlot.activeCancel = func() { wrongCalled = true }
	wrongSlot.mu.Unlock()
	defer fx.d.queue.release(wrongSlot)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if publisher.req.RequesterID != "user-1" {
		t.Fatalf("RequesterID = %q, want the triggering message's actor id", publisher.req.RequesterID)
	}
	if !publisher.req.LifecycleFeedback {
		t.Fatal("LifecycleFeedback = false, want durable outbox metadata propagated to publisher")
	}
	if publisher.req.Abort == nil {
		t.Fatal("Abort is nil, want a closure targeting the dispatch's session queue slot")
	}
	if publisher.req.FinalAttempt {
		t.Fatal("FinalAttempt = true on the first dispatch attempt")
	}
	if !publisher.req.Abort() {
		t.Fatal("Abort() = false, want true: it should cancel the active slot keyed by agent.BuildGroupSessionKey(agent-1, group)")
	}
	if !correctCalled {
		t.Fatal("Abort() did not cancel the slot keyed by agent.BuildGroupSessionKey(agent-1, group)")
	}
	if wrongCalled {
		t.Fatal("Abort() cancelled the wrong agent's session slot")
	}
}

func TestExecuteDispatchMarksFinalPublishAttempt(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.maxAttempts = 1
	publisher := &capturingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if !publisher.req.FinalAttempt {
		t.Fatal("FinalAttempt = false, want true when the current attempt exhausts the dispatcher budget")
	}
}

func assertNoAgentGroupMessages(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&count); err != nil {
		t.Fatalf("count agent messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("agent group messages = %d, want 0", count)
	}
}
