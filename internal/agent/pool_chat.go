package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/pkg/ai"
)

// Chat sends a message in a session and streams back events.
// Internally: gets/creates runner, passes history, collects events,
// appends to session log, streams to caller.
func (p *Pool) Chat(ctx context.Context, sessionID string, message runner.MessageContent, opts ...ChatOption) <-chan runner.Event {
	out := make(chan runner.Event, 100)

	var co chatOptions
	for _, o := range opts {
		o(&co)
	}

	ctx = memory.WithSessionID(ctx, sessionID)

	sess, r, err := p.getOrCreateRunner(ctx, sessionID, co.model)
	if err != nil {
		go func() {
			out <- runner.Event{Err: fmt.Errorf("get runner: %w", err)}
			close(out)
		}()
		return out
	}

	// Set user and agent context from session metadata (loaded from DB).
	if sess.Info.UserID != 0 {
		ctx = memory.WithUserID(ctx, sess.Info.UserID)
	}
	agentID := sess.Info.AgentID
	if agentID == "" {
		agentID = p.agentID
	}
	if agentID != "" {
		ctx = memory.WithAgentID(ctx, agentID)
	}

	msgText := runner.MessageText(message)
	p.log.Debug("chat started", "session_id", sessionID, "message_len", len(msgText))

	// Auto-compact if the session has grown too large.
	if p.NeedsCompaction(sessionID) {
		p.log.Info("auto-compaction triggered", "session_id", sessionID)
		if summary, err := p.CompactSession(ctx, sessionID); err != nil {
			p.log.Warn("auto-compaction failed, continuing with full history",
				"session_id", sessionID, "error", err)
		} else {
			p.log.Info("auto-compaction succeeded", "session_id", sessionID,
				"summary_len", len(summary))
			// Re-acquire session and runner after compaction (runner was restarted).
			sess, r, err = p.getOrCreateRunner(ctx, sessionID, co.model)
			if err != nil {
				go func() {
					out <- runner.Event{Err: fmt.Errorf("get runner after compaction: %w", err)}
					close(out)
				}()
				return out
			}
		}
	}

	// Update last active timestamp.
	now := time.Now()
	p.mu.Lock()
	sess.Info.LastActive = now
	// Auto-title: use the first user message as the session title.
	if sess.Info.Title == "" && len(msgText) > 0 {
		title := msgText
		if len(title) > 60 {
			// Truncate at word boundary.
			if idx := strings.LastIndex(title[:60], " "); idx > 20 {
				title = title[:idx] + "…"
			} else {
				title = title[:60] + "…"
			}
		}
		sess.Info.Title = title
	}
	infoSnapshot := sess.Info
	p.mu.Unlock()

	// Persist updated session info (LastActive, Title).
	if err := p.mem.SaveInfo(context.Background(), infoSnapshot); err != nil {
		p.log.Warn("failed to save session info", "session_id", sessionID, "error", err)
	}

	// Store user message via memory engine.
	userMsg := ai.UserMessage{Content: message}
	if err := p.mem.Ingest(ctx, sessionID, userMsg); err != nil {
		p.log.Warn("memory ingest user message failed", "session_id", sessionID, "error", err)
	}

	// Assemble context within budget via memory engine.
	var history []ai.Message
	assembled, err := p.mem.Assemble(ctx, sessionID, p.compaction.MaxTokens, p.compaction.KeepTail)
	if err != nil {
		p.log.Warn("memory assemble failed", "session_id", sessionID, "error", err)
	} else {
		history = assembled
	}

	stream := r.Chat(ctx, history, message)

	go p.streamEvents(ctx, sessionID, stream, out)

	return out
}

// streamEvents reads runner events, persists messages to the memory engine,
// and forwards events to the output channel.
func (p *Pool) streamEvents(ctx context.Context, sessionID string, stream <-chan runner.Event, out chan<- runner.Event) {
	defer close(out)
	persistCtx := context.WithoutCancel(ctx)
	var textBuf strings.Builder
	for evt := range stream {
		if evt.Err != nil {
			// Persist any buffered text before returning on error.
			if textBuf.Len() > 0 {
				flushMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: textBuf.String()}}}
				if err := p.mem.Ingest(persistCtx, sessionID, flushMsg); err != nil {
					p.log.Warn("memory ingest error-flush failed", "session_id", sessionID, "error", err)
				}
			}
			out <- evt
			return
		}

		// Store messages emitted by runners (assistant turns with tool calls, tool results).
		if evt.Store != nil {
			// Flush buffered text before storing a non-text message.
			if textBuf.Len() > 0 {
				flushMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: textBuf.String()}}}
				if err := p.mem.Ingest(persistCtx, sessionID, flushMsg); err != nil {
					p.log.Warn("memory ingest text-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
			}
			if err := p.mem.Ingest(persistCtx, sessionID, evt.Store); err != nil {
				p.log.Warn("memory ingest store message failed", "session_id", sessionID, "error", err)
			}
		}

		// Tool-use events pass through without history storage.
		if evt.ToolUse != nil {
			out <- evt
			continue
		}

		// Text delta: buffer for persistence (only the final assembled message is ingested).
		if evt.Text != "" {
			textBuf.WriteString(evt.Text)
		}

		out <- evt
	}
	// Stream ended normally — persist the complete assistant message.
	if textBuf.Len() > 0 {
		finalMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: textBuf.String()}}}
		if err := p.mem.Ingest(persistCtx, sessionID, finalMsg); err != nil {
			p.log.Warn("memory ingest final message failed", "session_id", sessionID, "error", err)
		}
	}
}
