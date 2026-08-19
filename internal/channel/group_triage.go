package channel

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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
	if chain >= int(state.AgentChainHardLimit) || posts >= int64(state.MaxRepliesPerHumanTrigger) || rate >= int64(state.MaxAgentPostsPerMinute) {
		return false, "hard_cap", false
	}

	if mentioned(row.AgentID, envelope.Mentions) {
		return true, "mentioned", false
	}
	if row.Kind == "nudge" && envelope.NudgeTarget == row.AgentID {
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

func (d *GroupDispatcher) hasLiveGroupClaims(ctx context.Context, groupID string) bool {
	claims, err := d.q.ListLiveGroupClaims(ctx, groupID)
	return err == nil && len(claims) > 0
}

func mentioned(agentID string, mentions []pkgchannel.Mention) bool {
	for _, mention := range mentions {
		if mention.AgentID == agentID {
			return true
		}
	}
	return false
}

func resolvedMentions(mentions []pkgchannel.Mention) []pkgchannel.Mention {
	resolved := make([]pkgchannel.Mention, 0, len(mentions))
	for _, mention := range mentions {
		if mention.AgentID != "" {
			resolved = append(resolved, mention)
		}
	}
	return resolved
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
