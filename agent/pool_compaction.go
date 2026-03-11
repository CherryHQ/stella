package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/vaayne/anna/agent/runner"
)

// compactionPrompt is sent to the runner to generate a conversation summary
// that replaces old history. Based on the handoff pattern — the summary must
// be self-contained so the runner can continue without the original context.
const compactionPrompt = `Summarize the conversation so far so a fresh context window can continue the work. Use this structure (skip empty sections):

## Goal
[Original objective of this session]

## Progress
- [What was completed]
- [What was partially done]

## Key Decisions
- [Decision and why]

## Files Changed
- ` + "`path/to/file`" + ` — [what changed]

## Current State
[Where things stand — what works, what doesn't]

## Blockers / Gotchas
- [Issues, edge cases, or warnings]

## Next Steps
1. [Concrete next action]
2. [Follow-up action]

Guidelines:
- Be self-contained — the reader has NO access to the previous conversation.
- Be concise — only what's relevant. Skip empty sections.
- Focus on decisions and rationale, not the discussion that led to them.
- List concrete file paths with context, not just paths.
- State next steps as actionable tasks — clear enough to execute immediately.
- Do NOT use tools or ask questions. Just output the summary.`

// CompactSession summarizes the conversation via the runner, rewrites the
// session file with a compaction entry + recent messages, and restarts the
// runner so it picks up the clean context.
//
// It returns the summary text on success.
func (p *Pool) CompactSession(ctx context.Context, sessionID string) (string, error) {
	if p.store == nil {
		return "", fmt.Errorf("compaction requires a persistent store")
	}

	// Remember the session's original model so we can restore it after
	// compaction — the fast model is only for generating the summary.
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	origModel := ""
	if ok {
		origModel = sess.Model
	}
	p.mu.Unlock()

	sess, r, err := p.getOrCreateRunner(ctx, sessionID, p.fastModel)
	if err != nil {
		return "", fmt.Errorf("get runner: %w", err)
	}

	// If the session was new (no prior model), fall back to the pool default.
	if origModel == "" {
		origModel = p.defaultModel
	}

	p.mu.Lock()
	events := make([]runner.RPCEvent, len(sess.Events))
	copy(events, sess.Events)
	p.mu.Unlock()

	p.log.Info("compaction started", "session_id", sessionID, "events", len(events))

	// Ask the runner to summarize the conversation.
	summary, err := p.collectFullResponse(ctx, r, events, compactionPrompt)
	if err != nil {
		return "", fmt.Errorf("generate summary: %w", err)
	}

	// Rewrite the session file with compaction.
	keepTail := p.compaction.KeepTail
	if keepTail == 0 {
		keepTail = 20
	}
	newEvents, err := p.store.Compact(sessionID, summary, keepTail)
	if err != nil {
		return "", fmt.Errorf("compact store: %w", err)
	}

	// Replace in-memory events.
	p.mu.Lock()
	sess.Events = newEvents
	p.mu.Unlock()

	// Kill the runner so it restarts with clean context on next Chat() —
	// unless the runner is stateful (maintains its own in-process context),
	// in which case killing it would lose context for no benefit. The
	// compacted history is persisted to disk for crash recovery either way.
	if sf, ok := r.(runner.Stateful); !ok || !sf.Stateful() {
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
		p.mu.Lock()
		sess.Runner = nil
		sess.Model = origModel // restore so next Chat uses the original model
		p.mu.Unlock()
	}

	p.log.Info("compaction complete", "session_id", sessionID,
		"summary_len", len(summary), "new_events", len(newEvents))

	return summary, nil
}

// collectFullResponse sends a message to a runner and collects the complete
// text response, blocking until the stream ends.
func (p *Pool) collectFullResponse(ctx context.Context, r runner.Runner, history []runner.RPCEvent, message string) (string, error) {
	stream := r.Chat(ctx, history, message)
	var buf strings.Builder
	for evt := range stream {
		if evt.Err != nil {
			return buf.String(), evt.Err
		}
		if evt.Text != "" {
			buf.WriteString(evt.Text)
		}
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("empty summary response")
	}
	return buf.String(), nil
}

// NeedsCompaction reports whether a session's estimated token count exceeds
// the compaction threshold. Returns false if compaction is disabled or no
// store is set.
func (p *Pool) NeedsCompaction(sessionID string) bool {
	if p.store == nil || p.compaction.MaxTokens <= 0 {
		return false
	}
	tokens, err := p.store.EstimateTokens(sessionID)
	if err != nil {
		p.log.Warn("failed to estimate tokens", "session_id", sessionID, "error", err)
		return false
	}
	return tokens > p.compaction.MaxTokens
}
