package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// Semantic arbiter prompt/latency budget. These are deliberately small: the
// arbiter runs on the no-mention group hot path, so a large prompt or a slow
// model would either time out (→ no response, worse than broadcast) or charge a
// tenant for routing every chatter message.
const (
	semanticMaxContextMessages = 6
	semanticPerMessageRunes    = 240
	semanticContextTotalRunes  = 1500
	semanticAgentsTotalRunes   = 1500
	semanticMemberSummaryRunes = 180
	semanticTimeout            = 2 * time.Second
	// semanticDefaultMaxResponders caps a broadcast decision. The rule arbiter's
	// no-mention cap is 1 (storm guard for the all-members fallback); a semantic
	// broadcast is an intentional multi-select, so it gets its own, larger cap.
	semanticDefaultMaxResponders = 5
)

// SemanticGroupMember is one candidate agent for a no-mention group decision.
// Scope/CreatorID drive routing-agent eligibility (whose model_fast and creds
// classify the message); they are credential/billing signals, not access checks.
type SemanticGroupMember struct {
	AgentID        string
	Name           string
	Scope          string
	CreatorID      string
	Summary        string // bounded public routing summary
	ReplyChannelID string
}

// SemanticGroupContextMessage is one prior group message, oldest→newest.
type SemanticGroupContextMessage struct {
	ActorType string
	ActorID   string
	Content   string
}

// SemanticGroupRequest is everything the arbiter needs to route a no-mention
// human message. The dispatcher owns DB access and builds this; the arbiter
// owns model I/O and policy.
type SemanticGroupRequest struct {
	Message       string
	RecentContext []SemanticGroupContextMessage
	Members       []SemanticGroupMember
	OwnerUserID   string // ctx_group_state.created_by_user_id; "" for platform groups
}

// SemanticGroupDecision is the strict outcome. RespondingAgents is always a
// subset of request member IDs, deduped and capped; empty when ShouldReply.
type SemanticGroupDecision struct {
	ShouldReply      bool
	RespondingAgents []string
	Reason           string
}

// SemanticGroupArbiter decides which agents (if any) should answer a no-mention
// group message. A nil decision (ShouldReply=false) means "stay silent" — the
// only safe fallback for an ambiguous classifier.
type SemanticGroupArbiter interface {
	Decide(ctx context.Context, req SemanticGroupRequest) SemanticGroupDecision
}

const semanticGroupPrompt = `You route a group chat message to the agents that should respond.
You are given the latest human message, recent context, and the list of agents in the group.
No agent was @mentioned, so decide from meaning alone.

Return JSON only: {"should_reply":bool,"agents":["agent_id",...],"reason":"short"}.

Rules:
- should_reply=false for chatter, small talk, or anything no agent needs to handle. Return empty agents.
- For a targeted question, pick the single best-matching agent by its description.
- For an explicit broadcast ("大家都说说", "everyone weigh in"), include the relevant agents.
- agents MUST be agent_id values from the provided list. Never invent IDs.
- Prefer silence when unsure. A wrong reply is worse than no reply.`

type LLMSemanticGroupArbiter struct {
	loadSnapshot  SnapshotLoader
	buildStream   StreamFuncBuilder
	complete      CompleteFunc
	timeout       time.Duration
	maxResponders int
	log           *slog.Logger
}

func NewLLMSemanticGroupArbiter(loadSnapshot SnapshotLoader, buildStream StreamFuncBuilder) *LLMSemanticGroupArbiter {
	return &LLMSemanticGroupArbiter{
		loadSnapshot:  loadSnapshot,
		buildStream:   buildStream,
		complete:      providers.Complete,
		timeout:       semanticTimeout,
		maxResponders: semanticDefaultMaxResponders,
		log:           slog.With("component", "semantic_group_arbiter"),
	}
}

func (a *LLMSemanticGroupArbiter) Decide(ctx context.Context, req SemanticGroupRequest) SemanticGroupDecision {
	if a == nil || a.loadSnapshot == nil || a.buildStream == nil || a.complete == nil {
		return SemanticGroupDecision{}
	}
	if strings.TrimSpace(req.Message) == "" || len(req.Members) == 0 {
		return SemanticGroupDecision{}
	}

	model, stream, ok := a.resolveRoutingModel(ctx, req)
	if !ok {
		a.debug("no eligible routing agent; staying silent")
		return SemanticGroupDecision{}
	}

	reqCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	msg, err := a.complete(reqCtx, model, ai.Context{
		System:   semanticGroupPrompt,
		Messages: []ai.Message{ai.UserMessage{Content: buildSemanticUserPayload(req)}},
	}, ai.CompleteOptions{StreamOptions: ai.StreamOptions{Timeout: a.timeout}}, stream)
	if err != nil {
		a.debug("semantic completion failed", "error", err)
		return SemanticGroupDecision{}
	}

	decision, err := parseSemanticDecision(ai.FlattenText(msg.Content))
	if err != nil {
		a.debug("semantic response parse failed", "error", err)
		return SemanticGroupDecision{}
	}
	return sanitizeSemanticDecision(decision, req, a.maxResponders)
}

// resolveRoutingModel picks the agent whose model_fast and credentials classify
// the message. Web groups prefer the group owner's own agent, then any
// system-scope agent; platform groups (no owner) allow system-scope only. This
// keeps a private user agent's credentials out of a shared routing decision.
func (a *LLMSemanticGroupArbiter) resolveRoutingModel(ctx context.Context, req SemanticGroupRequest) (ai.Model, providers.StreamFunc, bool) {
	if req.OwnerUserID != "" {
		for _, m := range req.Members {
			if m.CreatorID == req.OwnerUserID {
				if model, stream, ok := a.modelFor(ctx, m.AgentID); ok {
					return model, stream, true
				}
			}
		}
	}
	for _, m := range req.Members {
		if m.Scope == config.AgentScopeSystem {
			if model, stream, ok := a.modelFor(ctx, m.AgentID); ok {
				return model, stream, true
			}
		}
	}
	return ai.Model{}, nil, false
}

func (a *LLMSemanticGroupArbiter) modelFor(ctx context.Context, agentID string) (ai.Model, providers.StreamFunc, bool) {
	if agentID == "" {
		return ai.Model{}, nil, false
	}
	snap, err := a.loadSnapshot(ctx, agentID)
	if err != nil || snap == nil {
		return ai.Model{}, nil, false
	}
	if strings.TrimSpace(snap.ModelFast) == "" {
		return ai.Model{}, nil, false
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	if model.API == "" || model.ID == "" {
		return ai.Model{}, nil, false
	}
	creds := snap.ResolveProviderCreds(model.Provider)
	providerType := classifierProviderType(snap, model.Provider, creds)
	stream, err := a.buildStream(ctx, providerType, creds)
	if err != nil || stream == nil {
		return ai.Model{}, nil, false
	}
	return model, stream, true
}

func (a *LLMSemanticGroupArbiter) debug(msg string, args ...any) {
	if a != nil && a.log != nil {
		a.log.Debug(msg, args...)
	}
}

func buildSemanticUserPayload(req SemanticGroupRequest) string {
	var b strings.Builder
	b.WriteString("Agents:\n")
	for _, line := range semanticAgentLines(req.Members) {
		b.WriteString(line)
	}

	if ctxLines := recentContextLines(req.RecentContext); len(ctxLines) > 0 {
		b.WriteString("\nRecent context:\n")
		for _, line := range ctxLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\nLatest message:\n")
	b.WriteString(trimRunes(strings.TrimSpace(req.Message), semanticPerMessageRunes))
	return b.String()
}

func semanticAgentLines(members []SemanticGroupMember) []string {
	lines := make([]string, 0, len(members))
	total := 0
	for _, m := range members {
		line := semanticAgentLine(m)
		lineRunes := utf8.RuneCountInString(line)
		remaining := semanticAgentsTotalRunes - total
		if remaining <= 0 {
			break
		}
		if lineRunes > remaining {
			if len(lines) == 0 {
				lines = append(lines, trimRunes(line, remaining))
			}
			break
		}
		lines = append(lines, line)
		total += lineRunes
	}
	return lines
}

func semanticAgentLine(m SemanticGroupMember) string {
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(m.AgentID)
	if m.Name != "" {
		b.WriteString(" (")
		b.WriteString(m.Name)
		b.WriteString(")")
	}
	if s := trimRunes(strings.TrimSpace(m.Summary), semanticMemberSummaryRunes); s != "" {
		b.WriteString(": ")
		b.WriteString(s)
	}
	b.WriteString("\n")
	return b.String()
}

// recentContextLines keeps the last semanticMaxContextMessages messages
// (oldest→newest) and enforces both per-message and total rune budgets.
func recentContextLines(msgs []SemanticGroupContextMessage) []string {
	if len(msgs) > semanticMaxContextMessages {
		msgs = msgs[len(msgs)-semanticMaxContextMessages:]
	}
	lines := make([]string, 0, len(msgs))
	total := 0
	for _, m := range msgs {
		content := trimRunes(strings.TrimSpace(m.Content), semanticPerMessageRunes)
		if content == "" {
			continue
		}
		actor := m.ActorID
		if actor == "" {
			actor = m.ActorType
		}
		line := actor + ": " + content
		if total+utf8.RuneCountInString(line) > semanticContextTotalRunes {
			break
		}
		total += utf8.RuneCountInString(line)
		lines = append(lines, line)
	}
	return lines
}

func parseSemanticDecision(raw string) (SemanticGroupDecision, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SemanticGroupDecision{}, fmt.Errorf("empty semantic response")
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.Trim(raw, "`")
		raw = strings.TrimPrefix(raw, "json")
		raw = strings.TrimSpace(raw)
	}
	var payload struct {
		ShouldReply bool     `json:"should_reply"`
		Agents      []string `json:"agents"`
		Reason      string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return SemanticGroupDecision{}, fmt.Errorf("decode semantic response: %w", err)
	}
	return SemanticGroupDecision{
		ShouldReply:      payload.ShouldReply,
		RespondingAgents: payload.Agents,
		Reason:           payload.Reason,
	}, nil
}

// sanitizeSemanticDecision drops anything the model invented: only current group
// member IDs survive, duplicates are removed, and the result is capped. A
// should_reply=true with no surviving agents collapses to silence.
func sanitizeSemanticDecision(d SemanticGroupDecision, req SemanticGroupRequest, max int) SemanticGroupDecision {
	if !d.ShouldReply {
		return SemanticGroupDecision{Reason: d.Reason}
	}
	members := make(map[string]struct{}, len(req.Members))
	for _, m := range req.Members {
		members[m.AgentID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(d.RespondingAgents))
	var ids []string
	for _, id := range d.RespondingAgents {
		if id == "" {
			continue
		}
		if _, ok := members[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	ids = capResponders(ids, max)
	if len(ids) == 0 {
		return SemanticGroupDecision{Reason: d.Reason}
	}
	return SemanticGroupDecision{ShouldReply: true, RespondingAgents: ids, Reason: d.Reason}
}

func trimRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
