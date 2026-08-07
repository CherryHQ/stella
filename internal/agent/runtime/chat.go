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

const autoCompactionTimeout = 2 * time.Minute

// BeforeRunFunc is called before each chat turn to inject/override the system prompt.
type BeforeRunFunc func(ctx context.Context, info session.Info, model, msgText, system string, history []ai.Message) (systemOut string, err error)

// SnapshotPromptFunc builds a system prompt from the session's snapshot version.
type SnapshotPromptFunc func(ctx context.Context, info session.Info, snap memory.SessionSnapshot) string

// chat is retained for direct internal callers and tests. Runtime.ChatAdmitted
// uses chatWithRunner after synchronously selecting a runner at admission.
func (rt *Runtime) chat(ctx context.Context, out chan<- Event, info session.Info, msg MessageContent, co chatOptions) {
	ctx = withSessionIdentity(ctx, info)
	selection, err := rt.getOrCreateReservedRunner(ctx, info, co.model, co.extraTools)
	if err != nil {
		out <- Event{Err: fmt.Errorf("get runner: %w", err)}
		close(out)
		return
	}
	rt.chatWithRunner(ctx, out, info, msg, co, selection)
}

// chatWithRunner is the goroutine body for Runtime.Chat. The runner was selected and
// reserved synchronously by ChatAdmitted, so a policy invalidation cannot slip
// between admission and runner selection.
func (rt *Runtime) chatWithRunner(ctx context.Context, out chan<- Event, info session.Info, msg MessageContent, co chatOptions, selection runnerSelection) {
	defer rt.cache.releaseReservation(selection.session)

	isGuest := info.GuestID != ""
	ctx = withSessionIdentity(ctx, info)
	if info.GroupID != "" {
		// Group turns: carry the group id (not a user) so trusted adapters can mint
		// a confined GroupAgentActor. authz.WithUserID stays unset so runtime
		// identity remains the group (D9).
		if co.hasSpeaker {
			// Attach the speaker as a personalization target only.
			ctx = memory.WithCurrentSpeaker(ctx, co.currentSpeaker)
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
		close(out)
		return
	}

	msgText := MessageText(msg)
	rt.log.Debug("chat started", "session_id", info.ID, "message_len", len(msgText))

	// Fire PreAgentCall hooks for authenticated sessions only.
	chatStart := time.Now()
	var hookPlugins []hooks.HookPlugin
	if !isGuest {
		hookPlugins = rt.hookPlugins()
	}
	hs := hooks.NewHookSet(hookPlugins)
	hookMeta := hooks.HookMeta{
		SessionID: info.ID,
		UserID:    info.UserID,
		AgentID:   info.AgentID,
		Channel:   info.Channel,
	}
	if !isGuest {
		hs.RunPreAgentCall(ctx, &hooks.PreAgentCallContext{
			HookMeta:   hookMeta,
			MessageLen: len(msgText),
			Channel:    info.Channel,
		})
	}

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
			// The admission lease owns this runner and its metadata for the
			// full turn. Re-selecting through mutable cache state here would let
			// a concurrent non-terminal reset change beforeRun's model midway.
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
		if !isGuest {
			saveCtx = authz.WithUserID(saveCtx, info.UserID)
		}
		saveCtx = authz.WithAgentID(saveCtx, info.AgentID)
		// TouchActiveInfo, not SaveInfo: a `/new` rotation can archive this
		// session after the turn resolved it, and
		// auto-compaction above widens that window to minutes. SaveInfo would
		// replay the turn-start snapshot's `archived = false` and un-archive a
		// session the chat has already left; only main sessions have a
		// unique-active index to catch that, so a resurrected kind=chat row would
		// silently win its binding's newest-match lookup and pull the chat
		// backwards. TouchActiveInfo makes the check and the write one statement.
		if rec, err := updated.Record(); err != nil {
			rt.log.Warn("skip saving invalid session info", "session_id", info.ID, "error", err)
		} else if applied, err := sm.TouchActiveInfo(saveCtx, rec); err != nil {
			rt.log.Warn("failed to save session info", "session_id", info.ID, "error", err)
		} else if !applied {
			rt.log.Info("session archived mid-turn; skipping session info save", "session_id", info.ID)
		}
	}

	// Assemble history before appending the new user message.
	var history []ai.Message
	assembledOK := false
	assembled, err := rt.mem.Assemble(ctx, memSess, rt.compact.MaxTokens, rt.compact.KeepTail)
	if err != nil {
		rt.log.Warn("memory assemble failed", "session_id", info.ID, "error", err)
		if memSess.GroupID != "" {
			out <- Event{Err: fmt.Errorf("assemble group memory: %w", err)}
			close(out)
			return
		}
	} else {
		history = assembled
		assembledOK = true
	}

	// Resolve system prompt.
	baseSystem := co.systemOverride
	if isGuest {
		// A caller override is another capability surface. Guest runners always
		// use the minimal prompt selected from durable GuestID at construction.
		baseSystem = selection.runner.SystemPrompt()
	}
	if baseSystem == "" {
		baseSystem = selection.runner.SystemPrompt()
		if !isGuest && info.GroupID == "" && rt.snapshotPrompt != nil && info.UserID != "" && info.AgentID != "" {
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
	if !isGuest && rt.beforeRun != nil {
		systemOut, err := rt.beforeRun(ctx, info, selection.model, msgText, baseSystem, history)
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

	// Persist group trigger messages only after the turn succeeds. Otherwise a
	// failed durable dispatch retry would leave the same trigger in history and
	// duplicate it on the next attempt.
	userMsg := ai.UserMessage{Content: msg, Timestamp: time.Now()}
	modelMsg := userMsg
	var storePrefix []ai.Message
	if memSess.GroupID != "" {
		// Groups intentionally retain their legacy raw-image codec and append
		// timing until group-owned media receives its own authorization design.
		if co.hasSpeaker {
			modelMsg.Content = withCurrentSpeakerContext(msg, co.currentSpeaker)
		}
		storePrefix = []ai.Message{userMsg}
	} else {
		// Direct internal callers may supply either one image block or the usual
		// ordered block list. Existing canonical refs are not re-enriched.
		var blocks []ai.ContentBlock
		hasCanonicalImage := false
		switch content := userMsg.Content.(type) {
		case ai.ImageContent:
			blocks = []ai.ContentBlock{content}
		case ai.ImageRefContent:
			blocks = []ai.ContentBlock{content}
		case []ai.ContentBlock:
			blocks = content
		}
		if blocks != nil && !isGuest {
			if ai.HasImage(blocks) {
				if rt.sessionImages == nil {
					out <- Event{Err: errors.New("session image enrichment is not configured")}
					close(out)
					return
				}
				enriched, err := rt.sessionImages.Enrich(ctx, info.UserID, info.AgentID, blocks)
				if err != nil {
					out <- Event{Err: fmt.Errorf("enrich user images: %w", err)}
					close(out)
					return
				}
				blocks = enriched
			}
			hasCanonicalImage = ai.HasImageRef(blocks)
			userMsg.Content = blocks
			modelMsg = userMsg
		}
		if err := rt.mem.Append(ctx, memSess, userMsg); err != nil {
			if hasCanonicalImage {
				out <- Event{Err: fmt.Errorf("persist canonical user message: %w", err)}
				close(out)
				return
			}
			rt.log.Warn("memory append user message failed", "session_id", info.ID, "error", err)
		}
	}

	stream := selection.runner.Chat(ctx, history, modelMsg)
	chatErr := rt.streamEvents(ctx, info.ID, memSess, stream, out, hs, hookMeta, chatStart, storePrefix...)
	if chatErr == nil && assembledOK && ctx.Err() == nil && memSess.GroupID != "" {
		if committer, ok := rt.mem.(memory.GroupCursorCommitter); ok {
			commitCtx := context.WithoutCancel(ctx)
			if err := committer.CommitGroupCursor(commitCtx, memSess, memory.GroupSeqFromContext(ctx)); err != nil {
				rt.log.Warn("group cursor commit failed", "session_id", info.ID, "group_id", memSess.GroupID, "error", err)
			}
		}
	}
}

func (rt *Runtime) getOrCreateReservedRunner(ctx context.Context, info session.Info, model string, extraTools []tools.Tool) (runnerSelection, error) {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.conversation.id", info.ID),
		attribute.String("agent_id", info.AgentID),
	}
	if info.GuestID == "" {
		attrs = append(attrs, attribute.String("user_id", info.UserID))
	} else {
		attrs = append(attrs, attribute.String("guest_id", info.GuestID))
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

	selection, err := rt.cache.getOrCreateReserved(spanCtx, info, model, "", extraTools...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return runnerSelection{}, err
	}
	if selection.model != "" {
		span.SetAttributes(attribute.String("gen_ai.response.model", selection.model))
	}
	return selection, nil
}

func withSessionIdentity(ctx context.Context, info session.Info) context.Context {
	switch {
	case info.GuestID != "":
		// Guest mode is derived exclusively from durable session metadata. Never
		// mint a Stella user identity for the guest's UUID-shaped owner key.
		return authz.WithGuestID(ctx, info.GuestID)
	case info.GroupID != "":
		return authz.WithGroupID(ctx, info.GroupID)
	default:
		return authz.WithUserID(ctx, info.UserID)
	}
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
	storePrefix ...ai.Message,
) error {
	persistCtx := context.WithoutCancel(ctx)
	isGroup := memSess.GroupID != ""
	var chatErr error
	var pendingStores []ai.Message
	var textBuf strings.Builder
	var reasoningBuf strings.Builder
	appendWithPrefix := func(msgs ...ai.Message) error {
		storeMessages := make([]ai.Message, 0, len(storePrefix)+len(msgs))
		storeMessages = append(storeMessages, storePrefix...)
		storeMessages = append(storeMessages, msgs...)
		storePrefix = nil
		if isGroup {
			return rt.mem.Append(persistCtx, memSess, storeMessages...)
		}
		return rt.mem.Append(persistCtx, memSess, storeMessages...)
	}
	storeCurrent := func(msgs ...ai.Message) error {
		if isGroup {
			pendingStores = append(pendingStores, msgs...)
			return nil
		}
		return appendWithPrefix(msgs...)
	}
	flushInterruptedAssistant := func() {
		if isGroup || (textBuf.Len() == 0 && reasoningBuf.Len() == 0) {
			return
		}
		if err := appendWithPrefix(bufferedAssistantMessage(textBuf.String(), reasoningBuf.String())); err != nil {
			rt.log.Warn("memory append interrupted assistant failed", "session_id", sessionID, "error", err)
		}
		textBuf.Reset()
		reasoningBuf.Reset()
	}
	defer func() {
		hs.RunPostAgentCall(ctx, &hooks.PostAgentCallContext{
			HookMeta: hookMeta,
			Duration: time.Since(chatStart),
			Error:    chatErr,
		})
		close(out)
	}()

	for evt := range stream {
		if evt.Err != nil {
			chatErr = evt.Err
			flushInterruptedAssistant()
			// Explicit stop and lifecycle shutdown are normal cancellation paths,
			// not failed turns to surface as an in-band chat error.
			if ctx.Err() != nil && errors.Is(evt.Err, context.Canceled) {
				return ctx.Err()
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
				flushInterruptedAssistant()
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
			flushInterruptedAssistant()
			return chatErr
		}
	}

	if ctx.Err() != nil {
		flushInterruptedAssistant()
		return ctx.Err()
	}
	if textBuf.Len() > 0 || reasoningBuf.Len() > 0 {
		pendingStores = append(pendingStores, bufferedAssistantMessage(textBuf.String(), reasoningBuf.String()))
	}
	if isGroup {
		if len(storePrefix) > 0 || len(pendingStores) > 0 {
			if err := appendWithPrefix(pendingStores...); err != nil {
				rt.log.Warn("memory append final message failed", "session_id", sessionID, "error", err)
				return fmt.Errorf("memory append final message: %w", err)
			}
		}
		return nil
	}
	if len(pendingStores) > 0 {
		if err := appendWithPrefix(pendingStores...); err != nil {
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
