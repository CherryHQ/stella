package channel

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// triageWake is the deterministic admission gate in front of a full turn. It
// answers "may this agent run", never "should this agent speak": the only thing
// qualified to judge that is the agent itself, which sees the whole transcript
// and may end its turn with PASS. Every rule here is either a hard cap or an
// addressing fact, so an unclassifiable wake runs rather than being silenced by
// a model that knows less than the one it is gating.
func (d *GroupDispatcher) triageWake(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState, envelope GroupOutboxEnvelope) (act bool, reason string, degraded bool) {
	lastHuman, err := d.q.LastHumanSeqAtOrBefore(ctx, sqlc.LastHumanSeqAtOrBeforeParams{GroupID: row.GroupID, TriggerSeq: row.TriggerSeq})
	if err != nil {
		return false, "triage_db_error", true
	}
	posts, err := d.q.CountAgentPostsSinceSeq(ctx, sqlc.CountAgentPostsSinceSeqParams{GroupID: row.GroupID, AfterSeq: lastHuman})
	if err != nil {
		return false, "triage_db_error", true
	}
	rate, err := d.q.CountAgentPostsInWindow(ctx, sqlc.CountAgentPostsInWindowParams{GroupID: row.GroupID, AgentID: row.AgentID, Since: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		return false, "triage_db_error", true
	}
	chain := d.consecutiveAgentMessages(ctx, row.GroupID, message.Seq)
	if capped, reason := exceedsGroupHardCap(
		groupCapCheck{count: int64(chain), limit: int64(state.AgentChainHardLimit)},
		groupCapCheck{count: posts, limit: int64(state.MaxRepliesPerHumanTrigger)},
		groupCapCheck{count: rate, limit: int64(state.MaxAgentPostsPerMinute)},
	); capped {
		return false, reason, false
	}

	// Read mentions since this agent's ingest watermark, not just from the
	// trigger envelope. Coalescing and HOLD can move a wake past the message
	// that addressed this agent. The cursor is the consumption boundary: a
	// PASS/accepted turn has seen the mention, while a superseded or held turn
	// has not.
	if d.mentionedSinceCursor(ctx, row) || mentionsAgent(envelope.Mentions, row.AgentID) {
		return true, "mentioned", false
	}
	if row.Kind == "nudge" && envelope.NudgeTarget == row.AgentID {
		// A nudge is never superseded, so an ordinary wake that was already in
		// flight can post the reply the nudge asks for. Running it anyway posts
		// the same work twice.
		if d.agentPostedSince(ctx, row) {
			return false, "nudge_moot", false
		}
		return true, "nudge", false
	}
	if len(resolvedMentions(envelope.Mentions)) > 0 {
		return false, "mentioned_peer", false
	}

	// An agent-only run gets one lap per participant: once this agent has
	// already spoken since the last human message, further peer chatter is not
	// grounds to wake it again. Live claims mean work is in flight, so the run
	// is not idle chatter and the floor stays open.
	if message.ActorType == string(eventlog.ActorAgent) && !d.hasLiveGroupClaims(ctx, row.GroupID) && d.agentRunLapped(ctx, row.GroupID, message.Seq, row.AgentID) {
		return false, "agent_lap", false
	}
	return true, "open_floor", false
}

// mentionedSinceCursor answers whether an unconsumed message addressed this
// agent. A read failure means "no": the wake still runs on the open floor, so
// a transient error costs the mention its rule, never the agent its turn.
func (d *GroupDispatcher) mentionedSinceCursor(ctx context.Context, row sqlc.CtxGroupDispatch) bool {
	found, err := d.q.AgentMentionedSinceCursor(ctx, sqlc.AgentMentionedSinceCursorParams{
		GroupID:    row.GroupID,
		AgentID:    row.AgentID,
		Pipeline:   memory.GroupIngestPipeline(row.AgentID),
		TriggerSeq: row.TriggerSeq,
	})
	return err == nil && found
}

// agentPostedSince answers whether the agent has spoken since its wake was
// created. A read failure means "no": the nudge still runs, because losing a
// nudge to a transient error leaves the group stalled, while running one extra
// turn only risks a duplicate the acceptance gate can still catch.
func (d *GroupDispatcher) agentPostedSince(ctx context.Context, row sqlc.CtxGroupDispatch) bool {
	posted, err := d.q.AgentPostedSinceSeq(ctx, sqlc.AgentPostedSinceSeqParams{
		GroupID:  row.GroupID,
		AgentID:  row.AgentID,
		AfterSeq: row.TriggerSeq,
	})
	return err == nil && posted
}

func (d *GroupDispatcher) hasLiveGroupClaims(ctx context.Context, groupID string) bool {
	claims, err := d.q.ListLiveGroupClaims(ctx, groupID)
	return err == nil && len(claims) > 0
}

func (d *GroupDispatcher) agentRunLapped(ctx context.Context, groupID string, beforeSeq int64, agentID string) bool {
	rows, err := d.q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{GroupID: groupID, BeforeSeq: beforeSeq, MaxCount: 64})
	if err != nil {
		return false
	}
	seen := make(map[string]struct{})
	for _, row := range rows { // newest -> oldest
		if row.ActorType == string(eventlog.ActorHuman) {
			break
		}
		if row.ActorType == string(eventlog.ActorAgent) {
			seen[row.ActorID] = struct{}{}
		}
	}
	_, alreadySpoke := seen[agentID]
	return alreadySpoke && len(seen) > 0
}

func (d *GroupDispatcher) consecutiveAgentMessages(ctx context.Context, groupID string, beforeSeq int64) int {
	rows, err := d.q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{GroupID: groupID, BeforeSeq: beforeSeq + 1, MaxCount: 64})
	if err != nil {
		return 0
	}
	count := 0
	for _, row := range rows {
		if row.ActorType != string(eventlog.ActorAgent) {
			break
		}
		count++
	}
	return count
}
