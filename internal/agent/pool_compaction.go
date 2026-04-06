package agent

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/pkg/memory"
)

// CompactSession delegates compaction to the memory engine and returns the
// summary text on success.
func (p *Pool) CompactSession(ctx context.Context, sessionID string) (string, error) {
	return p.compactSessionMemory(ctx, sessionID)
}

// compactSessionMemory delegates compaction to the memory provider.
func (p *Pool) compactSessionMemory(ctx context.Context, sessionID string) (string, error) {
	c, ok := p.mem.(memory.Compactor)
	if !ok {
		return "", fmt.Errorf("memory provider does not support compaction")
	}
	session := p.memorySession(sessionID)
	result, err := c.Compact(ctx, session, memory.CompactionFull)
	if err != nil {
		return "", fmt.Errorf("memory compact: %w", err)
	}
	p.log.Info("memory compaction complete",
		"session_id", sessionID,
		"leaf_summaries", result.LeafSummariesCreated,
		"condensed_summaries", result.CondensedSummariesCreated,
		"tokens_before", result.TokensBefore,
		"tokens_after", result.TokensAfter,
		"duration", result.Duration)
	return fmt.Sprintf("compacted: %d leaf + %d condensed summaries, %d->%d tokens",
		result.LeafSummariesCreated, result.CondensedSummariesCreated,
		result.TokensBefore, result.TokensAfter), nil
}

// NeedsCompaction reports whether a session's estimated token count exceeds
// the compaction threshold. Returns false if compaction is disabled or the
// memory provider does not support compaction.
func (p *Pool) NeedsCompaction(sessionID string) bool {
	if p.compaction.MaxTokens <= 0 {
		return false
	}
	c, ok := p.mem.(memory.Compactor)
	if !ok {
		return false
	}
	return c.NeedsCompaction(context.Background(), p.memorySession(sessionID), float64(p.compaction.MaxTokens))
}

// memorySession builds a memory.Session with full metadata from the in-memory session map.
func (p *Pool) memorySession(sessionID string) memory.Session {
	s := memory.Session{ID: sessionID, AgentID: p.agentID}
	p.mu.Lock()
	if sess, ok := p.sessions[sessionID]; ok {
		s.UserID = sess.Info.UserID
		s.Channel = sess.Info.Channel
	}
	p.mu.Unlock()
	return s
}
