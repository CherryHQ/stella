package channel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ArbiterConfig controls the group arbiter's behavior.
type ArbiterConfig struct {
	// MaxRepliesPerTrigger caps the number of agent replies a single human
	// message can produce. 0 means use the default (1).
	MaxRepliesPerTrigger int

	// DebounceWindow is the minimum time between two dispatch triggers for
	// the same group. Messages arriving within the window are silently skipped.
	// 0 means no debounce.
	DebounceWindow time.Duration
}

// Arbiter decides which agents should respond to a group message.
// v1 is purely rule-based (no LLM calls):
//   - @mentioned agent → always respond (bypass rules)
//   - no mention → channel default agent responds
//   - agent-origin messages → never trigger other agents (except explicit @handoff)
type Arbiter struct {
	cfg ArbiterConfig

	mu          sync.Mutex
	lastTrigger map[string]time.Time // group_id → last dispatch time
}

// NewArbiter creates a rule-based arbiter.
func NewArbiter(cfg ArbiterConfig) *Arbiter {
	if cfg.MaxRepliesPerTrigger <= 0 {
		cfg.MaxRepliesPerTrigger = 1
	}
	return &Arbiter{
		cfg:         cfg,
		lastTrigger: make(map[string]time.Time),
	}
}

// ArbiterDecision is the outcome of a Decide call.
type ArbiterDecision struct {
	RespondingAgents []string // agent IDs that should respond (in order)
	Debounced        bool     // true = message was suppressed by debounce window
}

// DecideOptions controls per-call arbiter behavior.
type DecideOptions struct {
	// AllMembersFallback: when true, if there is no @mention and no
	// channelAgentID, return all group members instead of empty.
	AllMembersFallback bool
	// DisableDebounce skips the in-memory fallback debounce for durable dispatch
	// replays. Mention routing already bypasses debounce.
	DisableDebounce bool
	// MaxRepliesPerTrigger is the durable per-group cap. A positive value
	// overrides the process default for this decision.
	MaxRepliesPerTrigger int
}

// Decide determines which agents should respond to a human message in a group.
// mentionedAgents are the agent IDs resolved from @mentions.
// groupMembers are all agents currently in the group.
// channelAgentID is the channel's default agent (fallback when no mention).
func (a *Arbiter) Decide(_ context.Context, groupID string, mentions []pkgchannel.Mention, groupMembers []GroupMember, channelAgentID string, opts ...DecideOptions) ArbiterDecision {
	var opt DecideOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	log := slog.With("component", "arbiter", "group_id", groupID)

	// Resolve mentions FIRST so explicit @mentions bypass debounce (CR-010).
	var responding []string
	mentioned := mentionedAgentIDs(mentions)
	if len(mentioned) > 0 {
		memberSet := make(map[string]struct{}, len(groupMembers))
		for _, m := range groupMembers {
			memberSet[m.AgentID] = struct{}{}
		}
		for _, id := range mentioned {
			if _, ok := memberSet[id]; ok {
				responding = append(responding, id)
			}
		}
	}

	mentionResponding := len(responding) > 0

	// Explicit @mention always responds — skip debounce entirely.
	if !mentionResponding {
		// No fallback agent → check AllMembersFallback or skip (CR-012).
		if channelAgentID == "" {
			if !opt.AllMembersFallback {
				log.Debug("no mention and no channel default agent, skipping")
				return ArbiterDecision{}
			}
			for _, m := range groupMembers {
				responding = append(responding, m.AgentID)
			}
		} else {
			// Fallback path: apply debounce only for non-mention responses.
			if a.cfg.DebounceWindow > 0 && !opt.DisableDebounce {
				a.mu.Lock()
				last := a.lastTrigger[groupID]
				now := time.Now()
				if !last.IsZero() && now.Sub(last) < a.cfg.DebounceWindow {
					a.mu.Unlock()
					log.Debug("debounced", "since_last", now.Sub(last))
					return ArbiterDecision{Debounced: true}
				}
				a.lastTrigger[groupID] = now
				a.evictExpired(now)
				a.mu.Unlock()
			}
			responding = append(responding, channelAgentID)
		}
	}

	if !mentionResponding {
		maxReplies := a.cfg.MaxRepliesPerTrigger
		if opt.MaxRepliesPerTrigger > 0 {
			maxReplies = opt.MaxRepliesPerTrigger
		}
		responding = capResponders(responding, maxReplies)
	}

	// Evict expired debounce entries on all paths (not just fallback).
	if a.cfg.DebounceWindow > 0 {
		a.mu.Lock()
		a.evictExpired(time.Now())
		a.mu.Unlock()
	}

	log.Debug("decision", "responding", responding, "mentioned", mentioned)
	return ArbiterDecision{RespondingAgents: responding}
}

// evictExpired removes debounce entries older than 2x the window.
// Must be called with a.mu held.
func (a *Arbiter) evictExpired(now time.Time) {
	cutoff := now.Add(-2 * a.cfg.DebounceWindow)
	for k, t := range a.lastTrigger {
		if t.Before(cutoff) {
			delete(a.lastTrigger, k)
		}
	}
}

// capResponders truncates ids to at most max entries. max <= 0 means cap at 1,
// matching NewArbiter's default. Shared by the rule arbiter and the semantic
// no-mention path so broadcast decisions can never exceed the per-trigger cap.
func capResponders(ids []string, max int) []string {
	if max <= 0 {
		max = 1
	}
	if len(ids) > max {
		return ids[:max]
	}
	return ids
}

func mentionedAgentIDs(mentions []pkgchannel.Mention) []string {
	var ids []string
	seen := make(map[string]struct{})
	for _, m := range mentions {
		if m.AgentID == "" {
			continue
		}
		if _, ok := seen[m.AgentID]; ok {
			continue
		}
		seen[m.AgentID] = struct{}{}
		ids = append(ids, m.AgentID)
	}
	return ids
}
