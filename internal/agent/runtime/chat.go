package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// ErrChatTimeout is emitted when a chat turn exceeds its deadline.
var ErrChatTimeout = agenterr.ErrChatTimeout

const autoCompactionTimeout = 2 * time.Minute

// BeforeRunFunc is called before each chat turn to inject/override the system prompt.
type BeforeRunFunc func(ctx context.Context, info session.Info, model, msgText, system string, history []ai.Message) (systemOut string, err error)

// SnapshotPromptFunc builds a system prompt from the session's snapshot version.
type SnapshotPromptFunc func(ctx context.Context, info session.Info, snap memory.SessionSnapshot) string

// chat is the goroutine body for Runtime.Chat.
func (rt *Runtime) chat(ctx context.Context, out chan<- Event, info session.Info, msg MessageContent, co chatOptions) {
	cs, r, err := rt.cache.getOrCreate(ctx, info, co.model)
	if err != nil {
		out <- Event{Err: fmt.Errorf("get runner: %w", err)}
		close(out)
		return
	}

	ctx = memory.WithUserID(ctx, info.UserID)
	ctx = memory.WithAgentID(ctx, info.AgentID)
	if info.ProjectID != "" {
		ctx = memory.WithProjectID(ctx, info.ProjectID)
	}
	if info.Channel != "" {
		ctx = withChannel(ctx, info.Channel)
	}

	memSess := memory.Session{
		ID:      info.ID,
		AgentID: info.AgentID,
		UserID:  info.UserID,
		Channel: info.Channel,
	}

	msgText := MessageText(msg)
	rt.log.Debug("chat started", "session_id", info.ID, "message_len", len(msgText))

	// Fire PreAgentCall hook.
	chatStart := time.Now()
	hookPlugins := rt.hookPlugins()
	hs := hooks.NewHookSet(hookPlugins)
	hookMeta := hooks.HookMeta{
		SessionID: info.ID,
		UserID:    info.UserID,
		AgentID:   info.AgentID,
		Channel:   info.Channel,
	}
	hs.RunPreAgentCall(ctx, &hooks.PreAgentCallContext{
		HookMeta:   hookMeta,
		MessageLen: len(msgText),
		Channel:    info.Channel,
	})

	// Auto-compact.
	if rt.needsCompaction(ctx, memSess) {
		rt.log.Info("auto-compaction triggered", "session_id", info.ID)
		compactCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoCompactionTimeout)
		if summary, err := rt.compact_(compactCtx, memSess); err != nil {
			cancel()
			rt.log.Warn("auto-compaction failed", "session_id", info.ID, "error", err)
		} else {
			cancel()
			rt.log.Info("auto-compaction succeeded", "session_id", info.ID, "summary_len", len(summary))
			cs, r, err = rt.cache.getOrCreate(ctx, info, co.model)
			if err != nil {
				out <- Event{Err: fmt.Errorf("get runner after compaction: %w", err)}
				close(out)
				return
			}
		}
	}

	// Update last-active and title via SessionManager if available.
	now := time.Now()
	if sm, ok := rt.mem.(memory.SessionManager); ok {
		updated := info
		updated.LastActive = now
		if updated.Title == "" && len(msgText) > 0 {
			updated.Title = autoTitle(msgText)
		}
		saveCtx := memory.WithUserID(ctx, info.UserID)
		saveCtx = memory.WithAgentID(saveCtx, info.AgentID)
		if err := sm.SaveInfo(saveCtx, updated); err != nil {
			rt.log.Warn("failed to save session info", "session_id", info.ID, "error", err)
		}
	}

	// Assemble history before appending the new user message.
	var history []ai.Message
	assembled, err := rt.mem.Assemble(ctx, memSess, rt.compact.MaxTokens, rt.compact.KeepTail)
	if err != nil {
		rt.log.Warn("memory assemble failed", "session_id", info.ID, "error", err)
	} else {
		history = assembled
	}

	// Resolve system prompt.
	baseSystem := co.systemOverride
	if baseSystem == "" {
		baseSystem = r.SystemPrompt()
	}
	if rt.beforeRun != nil {
		systemOut, err := rt.beforeRun(ctx, info, cs.model, msgText, baseSystem, history)
		if err != nil {
			out <- Event{Err: fmt.Errorf("before run: %w", err)}
			close(out)
			return
		}
		if systemOut != "" {
			baseSystem = systemOut
		}
	}
	if baseSystem != "" {
		ctx = withSystemOverride(ctx, baseSystem)
	}

	// Exclude tools if requested.
	if len(co.excludedTools) > 0 {
		ctx = withExcludedTools(ctx, co.excludedTools...)
	}

	// Persist user message.
	userMsg := ai.UserMessage{Content: msg, Timestamp: time.Now()}
	if err := rt.mem.Append(ctx, memSess, userMsg); err != nil {
		rt.log.Warn("memory append user message failed", "session_id", info.ID, "error", err)
	}

	stream := r.Chat(ctx, history, userMsg)
	go rt.streamEvents(ctx, info.ID, memSess, stream, out, hs, hookMeta, chatStart)
}

func (rt *Runtime) hookPlugins() []hooks.HookPlugin {
	rt.cache.mu.Lock()
	fn := rt.cache.hooksFn
	rt.cache.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (rt *Runtime) needsCompaction(ctx context.Context, sess memory.Session) bool {
	if rt.compact.MaxTokens <= 0 {
		return false
	}
	c, ok := rt.mem.(memory.Compactor)
	if !ok {
		return false
	}
	return c.NeedsCompaction(ctx, sess, float64(rt.compact.MaxTokens))
}

func (rt *Runtime) compact_(ctx context.Context, sess memory.Session) (string, error) {
	c, ok := rt.mem.(memory.Compactor)
	if !ok {
		return "", fmt.Errorf("memory provider does not support compaction")
	}
	result, err := c.Compact(ctx, sess, memory.CompactionFull)
	if err != nil {
		return "", fmt.Errorf("memory compact: %w", err)
	}
	rt.log.Info("memory compaction complete",
		"session_id", sess.ID,
		"leaf_summaries", result.LeafSummariesCreated,
		"condensed_summaries", result.CondensedSummariesCreated,
		"tokens_before", result.TokensBefore,
		"tokens_after", result.TokensAfter)
	return fmt.Sprintf("compacted: %d leaf + %d condensed summaries, %d→%d tokens",
		result.LeafSummariesCreated, result.CondensedSummariesCreated,
		result.TokensBefore, result.TokensAfter), nil
}

// streamEvents reads runner events, persists messages, and forwards to the caller.
func (rt *Runtime) streamEvents(
	ctx context.Context,
	sessionID string,
	memSess memory.Session,
	stream <-chan Event,
	out chan<- Event,
	hs *hooks.HookSet,
	hookMeta hooks.HookMeta,
	chatStart time.Time,
) {
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
	var reasoningBuf strings.Builder

	for evt := range stream {
		if evt.Err != nil {
			chatErr = evt.Err
			if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
				flush := bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())
				if err := rt.mem.Append(persistCtx, memSess, flush); err != nil {
					rt.log.Warn("memory append error-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
				reasoningBuf.Reset()
			}
			if errors.Is(evt.Err, ErrChatTimeout) {
				notice := "I've been working on this for a while and have reached the time limit. Here's where things stand — feel free to send a message to continue or change direction."
				noticeMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: notice}}}
				if err := rt.mem.Append(persistCtx, memSess, noticeMsg); err != nil {
					rt.log.Warn("memory append timeout notice failed", "session_id", sessionID, "error", err)
				}
				out <- Event{Text: notice}
				out <- Event{Err: evt.Err}
				return
			}
			out <- evt
			return
		}

		if evt.Store != nil {
			if _, ok := evt.Store.(ai.AssistantMessage); ok {
				textBuf.Reset()
				reasoningBuf.Reset()
			} else if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
				flush := bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())
				if err := rt.mem.Append(persistCtx, memSess, flush); err != nil {
					rt.log.Warn("memory append text-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
				reasoningBuf.Reset()
			}
			if err := rt.mem.Append(persistCtx, memSess, evt.Store); err != nil {
				rt.log.Warn("memory append store message failed", "session_id", sessionID, "error", err)
			}
		}

		if evt.ToolUse != nil {
			out <- evt
			continue
		}

		if evt.Reasoning != "" {
			reasoningBuf.WriteString(evt.Reasoning)
		}
		if evt.Text != "" {
			textBuf.WriteString(evt.Text)
		}
		out <- evt
	}

	if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
		final := bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())
		if err := rt.mem.Append(persistCtx, memSess, final); err != nil {
			rt.log.Warn("memory append final message failed", "session_id", sessionID, "error", err)
		}
	}
}

func bufferedAssistantMessage(text, reasoning string) ai.AssistantMessage {
	blocks := make([]ai.ContentBlock, 0, 2)
	if reasoning != "" {
		blocks = append(blocks, ai.ThinkingContent{Thinking: reasoning})
	}
	if text != "" {
		blocks = append(blocks, ai.TextContent{Text: text})
	}
	return ai.AssistantMessage{Content: blocks}
}

func autoTitle(msgText string) string {
	title := msgText
	if len(title) > 60 {
		if idx := strings.LastIndex(title[:60], " "); idx > 20 {
			title = title[:idx] + "…"
		} else {
			title = title[:60] + "…"
		}
	}
	return title
}

// --- context helpers --------------------------------------------------------

func withSystemOverride(ctx context.Context, system string) context.Context {
	return agentctx.WithSystemOverride(ctx, system)
}

func withChannel(ctx context.Context, channel string) context.Context {
	return agentctx.WithChannel(ctx, channel)
}

func withExcludedTools(ctx context.Context, names ...string) context.Context {
	return agentctx.WithExcludedTools(ctx, names...)
}
