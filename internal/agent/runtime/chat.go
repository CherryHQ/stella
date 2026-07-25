package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ErrChatTimeout is emitted when a chat turn exceeds its deadline.
var ErrChatTimeout = agenterr.ErrChatTimeout

const (
	autoCompactionTimeout = 2 * time.Minute
	groupLCMMaxTokens     = 80_000
	groupLCMKeepTail      = 6
)

// BeforeRunFunc is called before each chat turn to inject/override the system prompt.
type BeforeRunFunc func(ctx context.Context, info session.Info, model, msgText, system string, history []ai.Message) (systemOut string, err error)

// SnapshotPromptFunc builds a system prompt from the session's snapshot version.
type SnapshotPromptFunc func(ctx context.Context, info session.Info, snap memory.SessionSnapshot) string

// chat is the goroutine body for Runtime.Chat.
func (rt *Runtime) chat(ctx context.Context, out chan<- Event, info session.Info, msg MessageContent, co chatOptions) {
	// A closed event stream means the complete turn, including durable group
	// cursor commit, has finished.
	defer close(out)

	cs, r, err := rt.getOrCreateRunner(ctx, info, co.model, co.extraTools)
	if err != nil {
		out <- Event{Err: fmt.Errorf("get runner: %w", err)}
		return
	}

	if info.GroupID == "" {
		ctx = authz.WithUserID(ctx, info.UserID)
	} else {
		// Group turns: carry the group id (not a user) so trusted adapters can mint
		// a confined GroupAgentActor. authz.WithUserID stays unset so runtime
		// identity remains the group (D9).
		ctx = authz.WithGroupID(ctx, info.GroupID)
		if co.hasSpeaker {
			speaker := co.currentSpeaker
			if rt.structuredGroupMemory {
				// Structured group turns retain only public actor metadata. The
				// private Stella-user link exists solely for legacy Profile fallback.
				speaker.UserID = ""
			}
			ctx = memory.WithCurrentSpeaker(ctx, speaker)
		}
	}
	ctx = authz.WithAgentID(ctx, info.AgentID)
	if info.ProjectID != "" {
		ctx = memory.WithProjectID(ctx, info.ProjectID)
	}
	if info.Channel != "" {
		ctx = withChannel(ctx, info.Channel)
	}

	memSess, err := info.MemoryScope()
	if err != nil {
		out <- Event{Err: fmt.Errorf("session scope: %w", err)}
		return
	}

	msgText := MessageText(msg)
	rt.log.Debug("chat started", "session_id", info.ID, "message_len", len(msgText))

	// Fire PreAgentCall hook.
	chatStart := time.Now()
	hookPlugins := rt.hookPlugins()
	hs := hooks.NewHookSet(hookPlugins)
	hookUserID := info.UserID
	if info.GroupID != "" {
		// Group sessions persist under a synthetic owner key, but hooks must not
		// interpret that key as an authenticated user identity.
		hookUserID = ""
	}
	hookMeta := hooks.HookMeta{
		SessionID: info.ID,
		UserID:    hookUserID,
		AgentID:   info.AgentID,
		Channel:   info.Channel,
	}
	hs.RunPreAgentCall(ctx, &hooks.PreAgentCallContext{
		HookMeta:   hookMeta,
		MessageLen: len(msgText),
		Channel:    info.Channel,
	})

	// Public group events must enter the per-agent LCM before compaction. The
	// triggering event itself remains excluded until the turn succeeds.
	var groupCommitter memory.GroupCursorCommitter
	if memSess.GroupID != "" {
		ingestor, ok := rt.mem.(memory.GroupEventIngestor)
		if !ok {
			out <- Event{Err: errors.New("memory provider does not support group event ingestion")}
			return
		}
		groupCommitter, ok = rt.mem.(memory.GroupCursorCommitter)
		if !ok {
			out <- Event{Err: errors.New("memory provider does not support group cursor commits")}
			return
		}
		if err := ingestor.SyncGroupEventsBefore(ctx, memSess, memory.GroupSeqFromContext(ctx)); err != nil {
			out <- Event{Err: fmt.Errorf("sync group events: %w", err)}
			return
		}
	}

	maxTokens, keepTail := rt.compactionLimits(memSess)

	// Auto-compact.
	if rt.needsCompaction(ctx, memSess, maxTokens) {
		rt.log.Info("auto-compaction triggered", "session_id", info.ID)
		compactCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoCompactionTimeout)
		if summary, err := rt.compact_(compactCtx, memSess); err != nil {
			cancel()
			rt.log.Warn("auto-compaction failed", "session_id", info.ID, "error", err)
		} else {
			cancel()
			rt.log.Info("auto-compaction succeeded", "session_id", info.ID, "summary_len", len(summary))
			cs, r, err = rt.getOrCreateRunner(ctx, info, co.model, co.extraTools)
			if err != nil {
				out <- Event{Err: fmt.Errorf("get runner after compaction: %w", err)}
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
		saveCtx := ctx
		if info.GroupID == "" {
			// Group sessions use UserID only as a durable storage owner. Keep
			// authz user identity unset when persisting group metadata.
			saveCtx = authz.WithUserID(saveCtx, info.UserID)
		}
		saveCtx = authz.WithAgentID(saveCtx, info.AgentID)
		if rec, err := updated.Record(); err != nil {
			rt.log.Warn("skip saving invalid session info", "session_id", info.ID, "error", err)
		} else if err := sm.SaveInfo(saveCtx, rec); err != nil {
			rt.log.Warn("failed to save session info", "session_id", info.ID, "error", err)
		}
	}

	// Assemble history before appending the new user message.
	var history []ai.Message
	assembledOK := false
	assembled, err := rt.mem.Assemble(ctx, memSess, maxTokens, keepTail)
	if err != nil {
		rt.log.Warn("memory assemble failed", "session_id", info.ID, "error", err)
		if memSess.GroupID != "" {
			out <- Event{Err: fmt.Errorf("assemble group memory: %w", err)}
			return
		}
	} else {
		history = assembled
		assembledOK = true
	}

	// Resolve system prompt.
	baseSystem := co.systemOverride
	if baseSystem == "" {
		baseSystem = r.SystemPrompt()
		if info.GroupID == "" && rt.snapshotPrompt != nil && info.UserID != "" && info.AgentID != "" {
			// DM per-turn snapshot prompt: rebuild system with frozen memory
			// version. Skipped when systemOverride is set (e.g. delegate custom
			// system).
			sss, ok := rt.mem.(memory.SessionSnapshotStore)
			if ok {
				snap, err := sss.GetOrCreateSessionSnapshot(ctx, info.ID, info.UserID, info.AgentID)
				if err != nil {
					rt.log.Warn("snapshot lookup failed, using base system", "session_id", info.ID, "error", err)
				} else {
					baseSystem = rt.snapshotPrompt(ctx, info, snap)
				}
			}
		}
	}
	if rt.beforeRun != nil {
		systemOut, err := rt.beforeRun(ctx, info, cs.model, msgText, baseSystem, history)
		if err != nil {
			out <- Event{Err: fmt.Errorf("before run: %w", err)}
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

	// Persist group trigger messages only after the turn succeeds. Otherwise a
	// failed durable dispatch retry would leave the same trigger in history and
	// duplicate it on the next attempt.
	userMsg := ai.UserMessage{Content: msg, Timestamp: time.Now()}
	modelMsg := userMsg
	if memSess.GroupID != "" && co.hasSpeaker {
		modelMsg.Content = withCurrentSpeakerContext(msg, co.currentSpeaker, !rt.structuredGroupMemory)
	}
	if memSess.GroupID == "" {
		if err := rt.mem.Append(ctx, memSess, userMsg); err != nil {
			rt.log.Warn("memory append user message failed", "session_id", info.ID, "error", err)
		}
	}

	stream := r.Chat(ctx, history, modelMsg)
	chatErr := rt.streamEvents(
		ctx,
		info.ID,
		memSess,
		stream,
		out,
		hs,
		hookMeta,
		chatStart,
		memory.GroupMessageIDFromContext(ctx),
		userMsg,
	)
	if chatErr == nil && assembledOK && ctx.Err() == nil && memSess.GroupID != "" {
		commitCtx := context.WithoutCancel(ctx)
		if err := groupCommitter.CommitGroupCursor(commitCtx, memSess, memory.GroupSeqFromContext(ctx)); err != nil {
			rt.log.Warn("group cursor commit failed", "session_id", info.ID, "group_id", memSess.GroupID, "error", err)
		}
	}
}

func (rt *Runtime) getOrCreateRunner(ctx context.Context, info session.Info, model string, extraTools []tools.Tool) (*cachedSession, Runner, error) {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.conversation.id", info.ID),
		attribute.String("user_id", info.UserID),
		attribute.String("agent_id", info.AgentID),
	}
	if info.ProjectID != "" {
		attrs = append(attrs, attribute.String("project_id", info.ProjectID))
	}
	if info.Channel != "" {
		attrs = append(attrs, attribute.String("stella.chat.channel", info.Channel))
	}
	if model != "" {
		attrs = append(attrs, attribute.String("gen_ai.request.model", model))
	}

	spanCtx, span := otel.Tracer("stella").Start(ctx, "agent.runner_get_or_create", trace.WithAttributes(attrs...))
	defer span.End()

	cs, r, err := rt.cache.getOrCreate(spanCtx, info, model, "", extraTools...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}
	if cs != nil && cs.model != "" {
		span.SetAttributes(attribute.String("gen_ai.response.model", cs.model))
	}
	return cs, r, nil
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

func (rt *Runtime) compactionLimits(sess memory.Session) (maxTokens, keepTail int) {
	if sess.GroupID != "" {
		return groupLCMMaxTokens, groupLCMKeepTail
	}
	return rt.compact.MaxTokens, rt.compact.KeepTail
}

func (rt *Runtime) needsCompaction(ctx context.Context, sess memory.Session, maxTokens int) bool {
	if maxTokens <= 0 {
		return false
	}
	c, ok := rt.mem.(memory.Compactor)
	if !ok {
		return false
	}
	return c.NeedsCompaction(ctx, sess, float64(maxTokens))
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

// sendEvent forwards evt to out unless ctx is done. It returns false when the
// consumer has gone away (ctx cancelled), letting the forwarder stop instead of
// blocking on a channel nobody drains — which would otherwise wedge the upstream
// runner goroutine indefinitely.
func sendEvent(ctx context.Context, out chan<- Event, evt Event) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case out <- evt:
		return true
	case <-ctx.Done():
		return false
	}
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
	groupMessageID string,
	groupTrigger ai.Message,
) error {
	persistCtx := context.WithoutCancel(ctx)
	isGroup := memSess.GroupID != ""
	var chatErr error
	var pendingStores []ai.Message
	storeCurrent := func(msgs ...ai.Message) error {
		if isGroup {
			pendingStores = append(pendingStores, msgs...)
			return nil
		}
		return rt.mem.Append(persistCtx, memSess, msgs...)
	}
	defer func() {
		hs.RunPostAgentCall(ctx, &hooks.PostAgentCallContext{
			HookMeta: hookMeta,
			Duration: time.Since(chatStart),
			Error:    chatErr,
		})
	}()

	var textBuf strings.Builder
	var reasoningBuf strings.Builder

	for evt := range stream {
		if evt.Err != nil {
			chatErr = evt.Err
			if !isGroup && (textBuf.Len() > 0 || reasoningBuf.Len() > 0) {
				flush := bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())
				if err := rt.mem.Append(persistCtx, memSess, flush); err != nil {
					rt.log.Warn("memory append error-flush failed", "session_id", sessionID, "error", err)
				}
				textBuf.Reset()
				reasoningBuf.Reset()
			}
			if errors.Is(evt.Err, ErrChatTimeout) {
				notice := "I've been working on this for a while and have reached the time limit. Here's where things stand — feel free to send a message to continue or change direction."
				if !isGroup {
					noticeMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: notice}}}
					if err := rt.mem.Append(persistCtx, memSess, noticeMsg); err != nil {
						rt.log.Warn("memory append timeout notice failed", "session_id", sessionID, "error", err)
					}
				}
				sendEvent(ctx, out, Event{Text: notice})
				return chatErr
			}
			sendEvent(ctx, out, evt)
			return chatErr
		}

		if evt.Store != nil {
			if _, ok := evt.Store.(ai.AssistantMessage); ok {
				textBuf.Reset()
				reasoningBuf.Reset()
			} else if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
				flush := bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())
				if err := storeCurrent(flush); err != nil {
					rt.log.Warn("memory append text-flush failed", "session_id", sessionID, "error", err)
					return fmt.Errorf("memory append text-flush: %w", err)
				}
				textBuf.Reset()
				reasoningBuf.Reset()
			}
			if err := storeCurrent(evt.Store); err != nil {
				rt.log.Warn("memory append store message failed", "session_id", sessionID, "error", err)
				return fmt.Errorf("memory append store message: %w", err)
			}
		}

		if evt.ToolUse != nil {
			if !sendEvent(ctx, out, evt) {
				chatErr = ctx.Err()
				return chatErr
			}
			continue
		}

		if evt.Reasoning != "" {
			reasoningBuf.WriteString(evt.Reasoning)
		}
		if evt.Text != "" {
			textBuf.WriteString(evt.Text)
		}
		if !sendEvent(ctx, out, evt) {
			chatErr = ctx.Err()
			return chatErr
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
		pendingStores = append(pendingStores, bufferedAssistantMessage(textBuf.String(), reasoningBuf.String()))
	}
	if isGroup {
		ingestor, ok := rt.mem.(memory.GroupEventIngestor)
		if !ok {
			return errors.New("memory provider does not support group event ingestion")
		}
		if err := ingestor.AppendGroupTurn(persistCtx, memSess, groupMessageID, groupTrigger, pendingStores...); err != nil {
			rt.log.Warn("memory append group turn failed", "session_id", sessionID, "error", err)
			return fmt.Errorf("memory append group turn: %w", err)
		}
		return nil
	}
	if len(pendingStores) > 0 {
		if err := rt.mem.Append(persistCtx, memSess, pendingStores...); err != nil {
			rt.log.Warn("memory append final message failed", "session_id", sessionID, "error", err)
			return fmt.Errorf("memory append final message: %w", err)
		}
	}
	return nil
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
