package channel

import (
	"context"
	"database/sql"
	"testing"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type stubSemanticArbiter struct {
	decision SemanticGroupDecision
	called   bool
	gotReq   SemanticGroupRequest
}

func (s *stubSemanticArbiter) Decide(_ context.Context, req SemanticGroupRequest) SemanticGroupDecision {
	s.called = true
	s.gotReq = req
	return s.decision
}

func addSecondMember(t *testing.T, fx dispatcherFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:                   "agent-2",
		Name:                 "Agent Two",
		Workspace:            t.TempDir(),
		Sandbox:              "{}",
		EnabledBuiltinSkills: "[]",
		Scope:                "system",
		Enabled:              1,
	}); err != nil {
		t.Fatalf("create agent-2: %v", err)
	}
	if err := fx.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      "ch-2",
		AgentID: sql.NullString{String: "agent-2", Valid: true},
	}); err != nil {
		t.Fatalf("create ch-2: %v", err)
	}
	if _, err := fx.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        "group-1",
		AgentID:        "agent-2",
		ReplyChannelID: "ch-2",
	}); err != nil {
		t.Fatalf("add agent-2 member: %v", err)
	}
}

// Explicit mentions must stay on the deterministic rule path; the semantic
// arbiter is never consulted.
func TestSemanticDispatchMentionBypassesArbiter(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "web", envelope)
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1"}}}
	fx.d.coord.semanticGroupArbiter = stub
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if stub.called {
		t.Fatal("semantic arbiter must not be called for mentioned messages")
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 1 {
		t.Fatalf("dispatch rows = %d, want 1 (mentioned agent)", count)
	}
}

// A no-mention message the arbiter declines produces no rows — even on web,
// where the legacy fallback would have broadcast to every member.
func TestSemanticDispatchNoReplyProducesZeroRows(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	addSecondMember(t, fx) // legacy web fallback would broadcast to both
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if !stub.called {
		t.Fatal("semantic arbiter must be called for no-mention web message")
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 0 {
		t.Fatalf("dispatch rows = %d, want 0 (arbiter declined)", count)
	}
}

// L1 routing applies even to mention-mode platform groups, where the legacy
// no-mention path would have stayed silent.
func TestSemanticDispatchTargetedInMentionMode(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	if _, err := fx.db.ExecContext(context.Background(), `UPDATE channel SET config = '{"group_mode":"mention"}' WHERE id = 'ch-1'`); err != nil {
		t.Fatalf("set mention mode: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1"}}}
	fx.d.coord.semanticGroupArbiter = stub
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 1 {
		t.Fatalf("dispatch rows = %d, want 1 (targeted L1 reply)", count)
	}
}

// A broadcast decision materializes one dispatch row per selected agent.
func TestSemanticDispatchBroadcastCreatesMultipleRows(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	addSecondMember(t, fx)
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1", "agent-2"}}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 2 {
		t.Fatalf("dispatch rows = %d, want 2 (broadcast)", count)
	}
}

// The arbiter collapses any failure to silence; the dispatcher honors that by
// creating no rows.
func TestSemanticDispatchFailureProducesZeroRows(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{}} // zero value = silence
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 0 {
		t.Fatalf("dispatch rows = %d, want 0 (failure → silence)", count)
	}
}

// The dispatcher feeds the arbiter the group owner id and bounded member
// metadata sourced from the agent rows.
func TestSemanticDispatchPassesOwnerAndMembers(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.ExecContext(context.Background(), `INSERT INTO auth_user (id, email) VALUES ('owner-9', 'owner-9@test')`); err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	if _, err := fx.db.ExecContext(context.Background(), `UPDATE ctx_group_state SET created_by_user_id = 'owner-9' WHERE id = 'group-1'`); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if stub.gotReq.OwnerUserID != "owner-9" {
		t.Fatalf("OwnerUserID = %q, want owner-9", stub.gotReq.OwnerUserID)
	}
	if len(stub.gotReq.Members) != 1 || stub.gotReq.Members[0].AgentID != "agent-1" || stub.gotReq.Members[0].Scope != "system" {
		t.Fatalf("members = %+v, want one system agent-1", stub.gotReq.Members)
	}
	if stub.gotReq.Message != "hello" {
		t.Fatalf("message = %q, want hello", stub.gotReq.Message)
	}
}
