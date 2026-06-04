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

// Decide determines which agents should respond to a human message in a group.
// mentionedAgents are the agent IDs resolved from @mentions.
// groupMembers are all agents currently in the group.
// channelAgentID is the channel's default agent (fallback when no mention).
func (a *Arbiter) Decide(_ context.Context, groupID string, mentions []pkgchannel.Mention, groupMembers []GroupMember, channelAgentID string) ArbiterDecision {
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

	// Explicit @mention always responds — skip debounce entirely.
	if len(responding) == 0 {
		// Fallback path: apply debounce only for non-mention responses.
		if a.cfg.DebounceWindow > 0 {
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

		if channelAgentID != "" {
			responding = append(responding, channelAgentID)
		}
	}

	if len(responding) > a.cfg.MaxRepliesPerTrigger {
		responding = responding[:a.cfg.MaxRepliesPerTrigger]
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
