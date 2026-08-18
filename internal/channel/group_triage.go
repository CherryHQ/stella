package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

const groupTriageTimeout = 5 * time.Second

const groupTriagePrompt = `You are one agent in a group chat. Given the latest message and recent context, is there something YOU should do now? Return JSON only: {"act":bool,"reason":"short"}.
Human involvement: lean act. Pure agent-to-agent chatter with no open work: lean silent. A peer explicitly waiting on your decision or action: act. Prefer silence when unsure.`

type GroupTriageRequest struct {
	AgentID, AgentName, GroupID, Platform, Message string
	ModelAgentID                                   string
	AuthorType                                     string
	Context                                        []string
}

type GroupTriage interface {
	Decide(context.Context, GroupTriageRequest) (bool, string, error)
}

// LLMGroupTriage classifies only the current agent's wake. It never selects
// peers, preserving local autonomy and credential isolation.
type LLMGroupTriage struct {
	load     SnapshotLoader
	build    StreamFuncBuilder
	complete CompleteFunc
	log      *slog.Logger
	mu       sync.Mutex
	warned   map[string]time.Time
}

func NewLLMGroupTriage(load SnapshotLoader, build StreamFuncBuilder) *LLMGroupTriage {
	return &LLMGroupTriage{load: load, build: build, complete: providers.Complete, log: slog.With("component", "group_triage"), warned: map[string]time.Time{}}
}

func (t *LLMGroupTriage) Decide(ctx context.Context, req GroupTriageRequest) (bool, string, error) {
	if t == nil || t.load == nil || t.build == nil || t.complete == nil {
		return false, "triage_unavailable", fmt.Errorf("triage unavailable")
	}
	modelAgentID := req.ModelAgentID
	if modelAgentID == "" {
		modelAgentID = req.AgentID
	}
	snap, err := t.load(ctx, modelAgentID)
	if err != nil || snap == nil {
		return false, "snapshot", fmt.Errorf("load snapshot: %w", err)
	}
	if strings.TrimSpace(snap.ModelFast) == "" {
		t.warnNoModel(req.GroupID)
		return false, "rules_only", nil
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.Provider)
	stream, err := t.build(ctx, classifierProviderType(snap, model.Provider, creds), creds)
	if err != nil || stream == nil {
		return false, "stream", fmt.Errorf("build stream: %w", err)
	}
	payload := fmt.Sprintf("Agent: %s (%s)\nAuthor type: %s\nRecent context:\n%s\nLatest message:\n%s", req.AgentID, req.AgentName, req.AuthorType, strings.Join(req.Context, "\n"), req.Message)
	cctx, cancel := context.WithTimeout(ctx, groupTriageTimeout)
	defer cancel()
	msg, err := t.complete(cctx, model, ai.Context{System: groupTriagePrompt, Messages: []ai.Message{ai.UserMessage{Content: payload}}}, ai.CompleteOptions{StreamOptions: ai.StreamOptions{Timeout: groupTriageTimeout}}, stream)
	if err != nil {
		return false, "completion", err
	}
	var result struct {
		Act    bool   `json:"act"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(ai.FlattenText(msg.Content))), &result); err != nil {
		return false, "malformed", err
	}
	return result.Act, result.Reason, nil
}

func (t *LLMGroupTriage) warnNoModel(groupID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.warned[groupID]) < time.Hour {
		return
	}
	t.warned[groupID] = time.Now()
	t.log.Warn("group triage has no fast model; using rules only", "group_id", groupID)
}

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
	if d.triage == nil {
		return false, "rules_only", false
	}
	a, err := d.q.GetAgent(ctx, row.AgentID)
	if err != nil {
		return false, "triage_agent_missing", true
	}
	modelAgentID := row.AgentID
	if state.Platform != "web" {
		modelAgentID = ""
		for _, member := range members {
			candidate, getErr := d.q.GetAgent(ctx, member.AgentID)
			if getErr == nil && candidate.Scope == config.AgentScopeSystem {
				modelAgentID = candidate.ID
				break
			}
		}
		if modelAgentID == "" {
			return false, "rules_only", false
		}
	}
	act, why, err := d.triage.Decide(ctx, GroupTriageRequest{AgentID: row.AgentID, ModelAgentID: modelAgentID, AgentName: a.Name, GroupID: row.GroupID, Platform: state.Platform, AuthorType: message.ActorType, Message: message.Content, Context: d.triageContext(ctx, row.GroupID, message.Seq, row.AgentID)})
	if err != nil {
		return false, "degraded_triage:" + why, true
	}
	if act {
		return true, "classifier:" + why, false
	}
	return false, "classifier_silent:" + why, false
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

func (d *GroupDispatcher) triageContext(ctx context.Context, groupID string, beforeSeq int64, agentID string) []string {
	rows, err := d.q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{GroupID: groupID, BeforeSeq: beforeSeq + 1, MaxCount: 6})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		marker := "NEW"
		if rows[i].ActorID == agentID {
			marker = "▸YOU"
		}
		out = append(out, fmt.Sprintf("%s %s: %s", marker, rows[i].ActorType, rows[i].Content))
	}
	return out
}
