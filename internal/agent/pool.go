package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/memory"
)

// Pool manages a set of sessions, each with its own history and runner.
// It is the only type channels interact with.
type Pool struct {
	factory      runner.NewRunnerFunc
	sessions     map[string]*Session
	mem          memory.Engine // memory engine — sole persistence layer
	mu           sync.Mutex
	idleTimeout  time.Duration
	compaction   CompactionConfig
	defaultModel string // default model ID for new runners
	fastModel    string // model ID used for compaction / fast tasks
	log          *slog.Logger
}

// NewPool creates a new Pool with the given runner factory and memory engine.
// The memory engine is required — it is the sole persistence layer for sessions.
func NewPool(factory runner.NewRunnerFunc, mem memory.Engine, opts ...PoolOption) *Pool {
	p := &Pool{
		factory:     factory,
		mem:         mem,
		sessions:    make(map[string]*Session),
		idleTimeout: 10 * time.Minute,
		log:         slog.With("component", "pool"),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// CreateSession creates a new session with a generated ID and persists its metadata.
func (p *Pool) CreateSession(channel string) (SessionInfo, error) {
	p.mu.Lock()
	info := p.createSessionLocked(channel)
	p.mu.Unlock()
	return p.persistNewSession(info)
}

// createSessionLocked creates a new session and adds it to the in-memory map.
// Caller must hold p.mu.
func (p *Pool) createSessionLocked(channel string) SessionInfo {
	now := time.Now()
	info := SessionInfo{
		ID:         channel + "-" + uuid.New().String()[:8],
		Channel:    channel,
		CreatedAt:  now,
		LastActive: now,
	}
	p.sessions[info.ID] = &Session{Info: info}
	return info
}

// persistNewSession saves session metadata to the memory engine and logs creation.
func (p *Pool) persistNewSession(info SessionInfo) (SessionInfo, error) {
	if err := p.mem.SaveInfo(context.Background(), info); err != nil {
		return info, fmt.Errorf("persist session info: %w", err)
	}
	p.log.Info("session created", "session_id", info.ID, "channel", info.Channel)
	return info, nil
}

// activeSessionLocked returns the most recent non-archived session for a channel
// from the in-memory map and persistent store. Caller must hold p.mu.
func (p *Pool) activeSessionLocked(channel string) (SessionInfo, bool) {
	var best *SessionInfo
	seen := make(map[string]struct{})

	// Check in-memory sessions (have the freshest LastActive).
	for _, sess := range p.sessions {
		if sess.Info.Archived || sess.Info.Channel != channel {
			continue
		}
		seen[sess.Info.ID] = struct{}{}
		if best == nil || sess.Info.LastActive.After(best.LastActive) {
			info := sess.Info
			best = &info
		}
	}

	// Check persistent store for sessions not yet in memory.
	items, err := p.mem.ListInfo(context.Background(), false)
	if err == nil {
		for _, info := range items {
			if info.Channel != channel {
				continue
			}
			if _, ok := seen[info.ID]; ok {
				continue
			}
			if best == nil || info.LastActive.After(best.LastActive) {
				info := info
				best = &info
			}
		}
	}

	if best == nil {
		return SessionInfo{}, false
	}
	return *best, true
}

// ActiveSession returns the most recent non-archived session for a channel.
func (p *Pool) ActiveSession(channel string) (SessionInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeSessionLocked(channel)
}

// ResolveSession returns the active session for a channel, creating one if needed.
// The check-and-create is atomic to prevent duplicate sessions under concurrent access.
func (p *Pool) ResolveSession(channel string) (SessionInfo, error) {
	p.mu.Lock()
	if info, ok := p.activeSessionLocked(channel); ok {
		p.mu.Unlock()
		return info, nil
	}
	info := p.createSessionLocked(channel)
	p.mu.Unlock()
	return p.persistNewSession(info)
}

// RotateSession archives the active session for a channel (if any) and creates a new one.
func (p *Pool) RotateSession(channel string) (SessionInfo, error) {
	if old, ok := p.ActiveSession(channel); ok {
		if err := p.ArchiveSession(old.ID); err != nil {
			p.log.Warn("archive failed during rotate", "session_id", old.ID, "error", err)
		}
	}
	return p.CreateSession(channel)
}

// GetSession returns metadata for a session.
func (p *Pool) GetSession(sessionID string) (SessionInfo, error) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	p.mu.Unlock()
	if ok {
		return sess.Info, nil
	}

	si, err := p.mem.LoadInfo(context.Background(), sessionID)
	if err == nil {
		return si, nil
	}
	return SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
}

// ListSessions returns metadata for all sessions.
func (p *Pool) ListSessions(includeArchived bool) ([]SessionInfo, error) {
	items, err := p.mem.ListInfo(context.Background(), includeArchived)
	if err != nil {
		return nil, err
	}
	result := make([]SessionInfo, len(items))
	copy(result, items)
	return result, nil
}

// ArchiveSession marks a session as archived, closes its runner, but keeps history on disk.
// The session is removed from the in-memory map; its metadata persists in the index.
func (p *Pool) ArchiveSession(sessionID string) error {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	var r runner.Runner
	if ok {
		r = sess.Runner
		delete(p.sessions, sessionID)
	}
	p.mu.Unlock()

	info, err := p.mem.LoadInfo(context.Background(), sessionID)
	if err == nil {
		info.Archived = true
		if err := p.mem.SaveInfo(context.Background(), info); err != nil {
			p.log.Warn("failed to persist archive", "session_id", sessionID, "error", err)
		}
	}

	if r != nil {
		if closer, ok := r.(io.Closer); ok {
			return closer.Close()
		}
	}

	p.log.Info("session archived", "session_id", sessionID)
	return nil
}

// History returns the message history for a session, loading from the memory engine.
// Returns nil if the session has no history.
func (p *Pool) History(sessionID string) []ai.Message {
	msgs, err := p.mem.Load(context.Background(), sessionID)
	if err == nil && len(msgs) > 0 {
		return msgs
	}
	return nil
}

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

	go func() {
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
	}()

	return out
}

// SetFactory replaces the runner factory used for new runners.
// Existing runners are not affected until their session is reset.
func (p *Pool) SetFactory(factory runner.NewRunnerFunc) {
	p.mu.Lock()
	p.factory = factory
	p.mu.Unlock()
}

// SetDefaultModel updates the default model used for new runners.
// Call this alongside SetFactory when the user switches models at runtime.
func (p *Pool) SetDefaultModel(model string) {
	p.mu.Lock()
	p.defaultModel = model
	p.mu.Unlock()
}

// Close shuts down all sessions and runners.
func (p *Pool) Close() error {
	p.mu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*Session)
	p.mu.Unlock()

	var lastErr error
	for id, sess := range sessions {
		p.log.Info("closing session", "session_id", id)
		if closer, ok := sess.Runner.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				lastErr = err
			}
		}
	}

	if err := p.mem.Close(); err != nil {
		lastErr = err
	}

	return lastErr
}

// getOrCreateRunner returns the session and its runner, creating both if needed.
// If the session is not in memory but exists on disk, its history is restored.
// If model is non-empty and differs from the session's current model, the
// existing runner is replaced.
func (p *Pool) getOrCreateRunner(ctx context.Context, sessionID string, model string) (*Session, runner.Runner, error) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	if ok && sess.Runner != nil {
		// Check if the runner is still alive (for runners that support liveness).
		if aliver, isAliver := sess.Runner.(runner.Aliver); isAliver && !aliver.Alive() {
			p.log.Warn("replacing dead runner", "session_id", sessionID)
			if closer, isCloser := sess.Runner.(io.Closer); isCloser {
				_ = closer.Close()
			}
			sess.Runner = nil
		}
	}
	if ok && sess.Runner != nil {
		// If a specific model was requested and it differs from the session's
		// current model, replace the runner.
		if model != "" && sess.Model != model {
			p.log.Info("switching model", "session_id", sessionID, "from", sess.Model, "to", model)
			if closer, isCloser := sess.Runner.(io.Closer); isCloser {
				_ = closer.Close()
			}
			sess.Runner = nil
		} else {
			p.mu.Unlock()
			return sess, sess.Runner, nil
		}
	}
	if !ok {
		sess = &Session{}
		p.sessions[sessionID] = sess

		// Restore metadata from memory engine if available.
		if info, err := p.mem.LoadInfo(context.Background(), sessionID); err == nil {
			sess.Info = info
		} else {
			sess.Info = SessionInfo{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now()}
		}
	}
	p.mu.Unlock()

	// Resolve the model: explicit > session's current > pool default.
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = sess.Model
	}
	if effectiveModel == "" {
		effectiveModel = p.defaultModel
	}

	r, err := p.factory(ctx, effectiveModel)
	if err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	sess.Runner = r
	sess.Model = effectiveModel
	p.mu.Unlock()

	// Memory: bootstrap the conversation for this session.
	if err := p.mem.Bootstrap(context.Background(), sessionID); err != nil {
		p.log.Warn("memory bootstrap failed", "session_id", sessionID, "error", err)
	}

	p.log.Info("created runner", "session_id", sessionID)
	return sess, r, nil
}
