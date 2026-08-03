package channel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

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
		ID:        "agent-2",
		Name:      "Agent Two",
		Workspace: t.TempDir(),
		Sandbox:   json.RawMessage("{}"),
		Scope:     "system",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("create agent-2: %v", err)
	}
	if err := fx.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      "ch-2",
		AgentID: pgtype.Text{String: "agent-2", Valid: true},
	}); err != nil {
		t.Fatalf("create ch-2: %v", err)
	}
	if _, err := fx.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        "11111111-1111-1111-1111-111111111111",
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

// A platform mention that resolves to no group member must not silently suppress
// the turn: it falls back to semantic routing so an explicit @mention is never
// less reliable than a no-mention message (#619).
func TestSemanticDispatchPlatformUnresolvedMentionFallsBackToArbiter(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{Raw: "@other-bot", PlatformID: "other-bot"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1"}}}
	fx.d.coord.semanticGroupArbiter = stub
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if !stub.called {
		t.Fatal("semantic arbiter must be called when a platform mention resolves to no member")
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 1 {
		t.Fatalf("dispatch rows = %d, want 1 (semantic fallback)", count)
	}
}

func TestSemanticDispatchUnresolvedMentionFallsBackToArbiter(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{Raw: "@ghost", PlatformID: "other-bot"}})
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
	if !stub.called {
		t.Fatal("semantic arbiter must be called when a mention resolves to no member")
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 1 {
		t.Fatalf("dispatch rows = %d, want 1 (semantic fallback)", count)
	}
}

// When a mention resolves to no member and no semantic arbiter is configured, a
// platform group still stays silent — the fallback only borrows the no-mention
// path, it does not invent a broadcast.
func TestSemanticDispatchUnresolvedMentionNoArbiterStaysSilent(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{Raw: "@ghost", PlatformID: "other-bot"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	// No semantic arbiter configured.
	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 0 {
		t.Fatalf("dispatch rows = %d, want 0 (no arbiter)", count)
	}
}

// The mention that DOES resolve still takes the deterministic path and never
// consults the semantic arbiter, even when other mentions in the same message
// fail to resolve.
func TestSemanticDispatchResolvedMentionSkipsArbiter(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{
		{Raw: "@ghost", PlatformID: "other-bot"},
		{Raw: "@agent-1", AgentID: "agent-1"},
	})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1"}}}
	fx.d.coord.semanticGroupArbiter = stub
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if stub.called {
		t.Fatal("semantic arbiter must not be called when a mention resolves to a member")
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

func TestSemanticDispatchWebNonMemberMentionTextUsesArbiter(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_message SET content = '@non-member hello' WHERE id = $1`, fx.message.ID); err != nil {
		t.Fatalf("set message content: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"agent-1"}}}
	fx.d.coord.semanticGroupArbiter = stub
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if !stub.called {
		t.Fatal("semantic arbiter must be called when web @text produced no envelope mention")
	}
	if stub.gotReq.Message != "@non-member hello" {
		t.Fatalf("semantic message = %q, want @non-member hello", stub.gotReq.Message)
	}
	if count, _ := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID); count != 1 {
		t.Fatalf("dispatch rows = %d, want 1 (semantic fallback)", count)
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

// L1 routing applies to platform groups, where the legacy no-mention path would
// have stayed silent.
func TestSemanticDispatchTargetedPlatformGroup(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
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
	if _, err := fx.db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ('99999999-0000-0000-0000-000000000009', 'owner-9@test')`); err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET created_by_user_id = '99999999-0000-0000-0000-000000000009' WHERE id = '11111111-1111-1111-1111-111111111111'`); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if stub.gotReq.OwnerUserID != "99999999-0000-0000-0000-000000000009" {
		t.Fatalf("OwnerUserID = %q, want owner-9", stub.gotReq.OwnerUserID)
	}
	if len(stub.gotReq.Members) != 1 || stub.gotReq.Members[0].AgentID != "agent-1" || stub.gotReq.Members[0].Scope != "system" {
		t.Fatalf("members = %+v, want one system agent-1", stub.gotReq.Members)
	}
	if stub.gotReq.Message != "hello" {
		t.Fatalf("message = %q, want hello", stub.gotReq.Message)
	}
}

func TestSemanticDispatchUsesSystemPromptAsSummary(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	const prompt = "You are a helpful coding assistant"
	if _, err := fx.db.Exec(context.Background(), `UPDATE agent SET system_prompt = $1 WHERE id = 'agent-1'`, prompt); err != nil {
		t.Fatalf("set system prompt: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if len(stub.gotReq.Members) != 1 {
		t.Fatalf("members = %+v, want one member", stub.gotReq.Members)
	}
	if stub.gotReq.Members[0].Summary != prompt {
		t.Fatalf("member summary = %q, want %q", stub.gotReq.Members[0].Summary, prompt)
	}
}

func TestSemanticDispatchPlatformOwnerIsNotForwarded(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	if _, err := fx.db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ('99999999-0000-0000-0000-000000000009', 'owner-9@test')`); err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET created_by_user_id = '99999999-0000-0000-0000-000000000009' WHERE id = '11111111-1111-1111-1111-111111111111'`); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if stub.gotReq.OwnerUserID != "" {
		t.Fatalf("OwnerUserID = %q, want empty for platform groups", stub.gotReq.OwnerUserID)
	}
}

func TestSemanticDispatchRecentContextExcludesFutureMessages(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	if _, err := fx.q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{
		ID:        "a1a1a1a1-0000-0000-0000-000000000000",
		GroupID:   "11111111-1111-1111-1111-111111111111",
		Seq:       0,
		ActorType: "human",
		ActorID:   "user-0",
		Content:   "past",
	}); err != nil {
		t.Fatalf("create past message: %v", err)
	}
	for _, msg := range []struct {
		id      string
		seq     int64
		content string
	}{
		{id: "a1a1a1a1-0000-0000-0000-000000000002", seq: 2, content: "future-2"},
		{id: "a1a1a1a1-0000-0000-0000-000000000003", seq: 3, content: "future-3"},
	} {
		if _, err := fx.q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{
			ID:        msg.id,
			GroupID:   "11111111-1111-1111-1111-111111111111",
			Seq:       msg.seq,
			ActorType: "human",
			ActorID:   "user-future",
			Content:   msg.content,
		}); err != nil {
			t.Fatalf("create future message %s: %v", msg.id, err)
		}
	}
	stub := &stubSemanticArbiter{decision: SemanticGroupDecision{ShouldReply: false}}
	fx.d.coord.semanticGroupArbiter = stub

	if err := fx.d.ProcessOutbox(ctx, fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if len(stub.gotReq.RecentContext) != 1 || stub.gotReq.RecentContext[0].Content != "past" {
		t.Fatalf("recent context = %+v, want only past message", stub.gotReq.RecentContext)
	}
}
