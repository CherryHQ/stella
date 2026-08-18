package channel

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// triageWake decides only whether this dispatch's agent may spend a full turn.
// Server-side acceptance still enforces freshness and caps after generation.
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

	members, err := d.q.ListGroupMembers(ctx, row.GroupID)
	if err != nil {
		return false, "triage_db_error", true
	}
	if state.Platform == "web" && message.ActorType == string(eventlog.ActorHuman) && len(members) == 1 {
		return true, "sole_web_agent", false
	}
	if message.ActorType == string(eventlog.ActorAgent) && d.agentRunLapped(ctx, row.GroupID, message.Seq, row.AgentID) {
		return false, "agent_lap", false
	}
	if d.coord == nil || d.coord.semanticGroupArbiter == nil {
		if state.Platform != "web" {
			d.log.Warn("degraded_triage: platform group has no system routing model", "group_id", row.GroupID)
		}
		return false, "no_classifier", false
	}
	// The legacy model adapter is constrained to this agent alone. It gives the
	// final routing classifier a per-agent act/silent question until the old
	// multi-select implementation is removed in the cutover cleanup.
	a, err := d.q.GetAgent(ctx, row.AgentID)
	if err != nil {
		return false, "triage_agent_missing", true
	}
	decision := d.coord.semanticGroupArbiter.Decide(ctx, SemanticGroupRequest{
		Message: message.Content, OwnerUserID: nullStringValue(state.CreatedByUserID),
		Members:       []SemanticGroupMember{{AgentID: row.AgentID, Name: a.Name, Scope: a.Scope, CreatorID: a.CreatorID, Summary: a.SystemPrompt}},
		RecentContext: d.recentGroupContext(ctx, d.q, row.GroupID, message.Seq),
	})
	if decision.ShouldReply && len(decision.RespondingAgents) == 1 && decision.RespondingAgents[0] == row.AgentID {
		return true, "classifier", false
	}
	return false, "classifier_silent", false
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

func (d *GroupDispatcher) recentGroupContext(ctx context.Context, q *sqlc.Queries, groupID string, currentSeq int64) []SemanticGroupContextMessage {
	rows, err := q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{GroupID: groupID, BeforeSeq: currentSeq, MaxCount: semanticMaxContextMessages})
	if err != nil {
		return nil
	}
	context := make([]SemanticGroupContextMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		context = append(context, SemanticGroupContextMessage{ActorType: rows[i].ActorType, ActorID: rows[i].ActorID, Content: rows[i].Content})
	}
	return context
}
