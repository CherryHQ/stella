package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const autoCompactionTimeout = 2 * time.Minute

// Chat sends a message in a session and streams back events.
// Internally: gets/creates runner, passes history, collects events,
// appends to session log, streams to caller.
func (p *Pool) Chat(ctx context.Context, sessionID string, message MessageContent, opts ...ChatOption) <-chan Event {
	out := make(chan Event, 100)

	var co chatOptions
	for _, o := range opts {
		o(&co)
	}

	ctx = memory.WithSessionID(ctx, sessionID)
	go p.chat(ctx, out, sessionID, message, co)
	return out
}

func (p *Pool) chat(ctx context.Context, out chan<- Event, sessionID string, message MessageContent, co chatOptions) {
	sess, r, err := p.getOrCreateRunner(ctx, sessionID, co.model)
	if err != nil {
		out <- Event{Err: fmt.Errorf("get runner: %w", err)}
		close(out)
		return
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
	if sess.Info.Channel != "" {
		ctx = WithChannel(ctx, sess.Info.Channel)
	}

	// Construct a Session value for all memory provider calls.
	memSession := memory.Session{
		ID:      sessionID,
		AgentID: agentID,
		UserID:  sess.Info.UserID,
		Channel: sess.Info.Channel,
	}

	msgText := MessageText(message)
	p.log.Debug("chat started", "session_id", sessionID, "message_len", len(msgText))

	// Fire PreAgentCall hook — marks the start of a chat request.
	chatStart := time.Now()
	hs := hooks.NewHookSet(p.hookPlugins())
	hookMeta := hooks.HookMeta{SessionID: sessionID, UserID: sess.Info.UserID, AgentID: agentID, Channel: sess.Info.Channel}
	hs.RunPreAgentCall(ctx, &hooks.PreAgentCallContext{
		HookMeta:   hookMeta,
		MessageLen: len(msgText),
		Channel:    sess.Info.Channel,
	})

	// Auto-compact if the session has grown too large. Use a short detached
	// context so a slow summarizer cannot consume the channel request deadline
	// and poison the rest of this chat turn with context.Canceled/DeadlineExceeded.
	if p.NeedsCompaction(sessionID) {
		p.log.Info("auto-compaction triggered", "session_id", sessionID)
		compactCtx, cancelCompact := context.WithTimeout(context.WithoutCancel(ctx), autoCompactionTimeout)
		if summary, err := p.CompactSession(compactCtx, sessionID); err != nil {
			cancelCompact()
			p.log.Warn("auto-compaction failed, continuing with full history",
				"session_id", sessionID, "error", err)
		} else {
			cancelCompact()
			p.log.Info("auto-compaction succeeded", "session_id", sessionID,
				"summary_len", len(summary))
			// Re-acquire session and runner after compaction (runner was restarted).
			sess, r, err = p.getOrCreateRunner(ctx, sessionID, co.model)
			if err != nil {
				out <- Event{Err: fmt.Errorf("get runner after compaction: %w", err)}
				close(out)
				return
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
	if sm, ok := p.mem.(memory.SessionManager); ok {
		if err := sm.SaveInfo(context.Background(), infoSnapshot); err != nil {
			p.log.Warn("failed to save session info", "session_id", sessionID, "error", err)
		}
	}

	// Assemble context within budget via memory engine.
	// NOTE: assemble BEFORE ingesting the current user message so the
	// assembled history does not include it — GoRunner.Chat appends
	// the message itself, which would otherwise create a duplicate.
	var history []ai.Message
	assembled, err := p.mem.Assemble(ctx, memSession, p.compaction.MaxTokens, p.compaction.KeepTail)
	if err != nil {
		p.log.Warn("memory assemble failed", "session_id", sessionID, "error", err)
	} else {
		history = assembled
	}

	// Session snapshot: build frozen system prompt using snapshot version.
	// Front-end (tool) writes advance the snapshot; back-end (Reflect) writes don't.
	var snapshotPrompt string
	if p.snapshotPromptFn != nil {
		if sss, ok := p.mem.(memory.SessionSnapshotStore); ok && sess.Info.UserID > 0 && agentID != "" {
			snapshot, snapErr := sss.GetOrCreateSessionSnapshot(ctx, sessionID, sess.Info.UserID, agentID)
			if snapErr != nil {
				p.log.Warn("session snapshot failed", "session_id", sessionID, "error", snapErr)
			} else {
				snapshotPrompt = p.snapshotPromptFn(ctx, sess.Info.UserID, agentID, snapshot)
			}
		}
	}

	if p.beforeRunFn != nil {
		baseSystem := snapshotPrompt
		if baseSystem == "" {
			if promter, ok := r.(SystemPrompter); ok {
				baseSystem = promter.SystemPrompt()
			}
		}
		if result, err := p.beforeRunFn(ctx, pkgplugins.BeforeRunContext{
			SessionID:    sessionID,
			Channel:      sess.Info.Channel,
			UserID:       sess.Info.UserID,
			AgentID:      agentID,
			Model:        sess.Model,
			MessageText:  msgText,
			SystemPrompt: baseSystem,
			History:      history,
		}); err != nil {
			out <- Event{Err: fmt.Errorf("before run: %w", err)}
			close(out)
			return
		} else if result.SystemPrompt != "" && result.SystemPrompt != baseSystem {
			ctx = WithSystemOverride(ctx, result.SystemPrompt)
		} else if baseSystem != "" {
			ctx = WithSystemOverride(ctx, baseSystem)
		}
	} else if snapshotPrompt != "" {
		ctx = WithSystemOverride(ctx, snapshotPrompt)
	}

	// Store user message via memory provider (after assembly to avoid duplication).
	userMsg := ai.UserMessage{Content: message, Timestamp: time.Now()}
	if err := p.mem.Append(ctx, memSession, userMsg); err != nil {
		p.log.Warn("memory append user message failed", "session_id", sessionID, "error", err)
	}

	stream := r.Chat(ctx, history, userMsg)

	go p.streamEvents(ctx, sessionID, memSession, stream, out, hs, hookMeta, chatStart)
}

// streamEvents reads runner events, persists messages to the memory provider,
// and forwards events to the output channel.
func (p *Pool) streamEvents(ctx context.Context, sessionID string, memSession memory.Session, stream <-chan Event, out chan<- Event, hs *hooks.HookSet, hookMeta hooks.HookMeta, chatStart time.Time) {
	var chatErr error
	defer func() {
		hs.RunPostAgentCall(ctx, &hooks.PostAgentCallContext{
			HookMeta: hookMeta,
			Duration: time.Since(chatStart),
			Error:    chatErr,
		})
		close(out)
	}()
	persistCtx := context.WithoutCancel(ctx)
	var textBuf strings.Builder
	for evt := range stream {
		if evt.Err != nil {
			chatErr = evt.Err
			// Persist any buffered text before returning on error.
			if textBuf.Len() > 0 {
				flushMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: textBuf.String()}}}
				if err := p.mem.Append(persistCtx, memSession, flushMsg); err != nil {
					p.log.Warn("memory append error-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
			}
			// On timeout, emit a friendly status notice instead of a raw error
			// so the user sees context rather than a crash. The session remains
			// intact; the user can send a new message to continue.
			if errors.Is(evt.Err, context.DeadlineExceeded) {
				notice := "I've been working on this for a while and have reached the time limit. Here's where things stand — feel free to send a message to continue or change direction."
				noticeMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: notice}}}
				if err := p.mem.Append(persistCtx, memSession, noticeMsg); err != nil {
					p.log.Warn("memory append timeout notice failed", "session_id", sessionID, "error", err)
				}
				out <- Event{Text: notice}
				return
			}
			out <- evt
			return
		}

		// Store messages emitted by runners (assistant turns with tool calls, tool results).
		if evt.Store != nil {
			// Flush buffered text before storing a non-text message.
			if textBuf.Len() > 0 {
				flushMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: textBuf.String()}}}
				if err := p.mem.Append(persistCtx, memSession, flushMsg); err != nil {
					p.log.Warn("memory append text-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
			}
			if err := p.mem.Append(persistCtx, memSession, evt.Store); err != nil {
				p.log.Warn("memory append store message failed", "session_id", sessionID, "error", err)
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
		if err := p.mem.Append(persistCtx, memSession, finalMsg); err != nil {
			p.log.Warn("memory append final message failed", "session_id", sessionID, "error", err)
		}
	}
}
