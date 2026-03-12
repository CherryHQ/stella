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
	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/memory"
	"github.com/vaayne/anna/store"
)

// Pool manages a set of sessions, each with its own history and runner.
// It is the only type channels interact with.
type Pool struct {
	factory      runner.NewRunnerFunc
	sessions     map[string]*Session
	store        store.Store
	mem          memory.Engine // memory engine for message persistence and compaction (optional)
	mu           sync.Mutex
	idleTimeout  time.Duration
	compaction   CompactionConfig
	defaultModel string // default model ID for new runners
	fastModel    string // model ID used for compaction / fast tasks
	log          *slog.Logger
}

// NewPool creates a new Pool with the given runner factory.
func NewPool(factory runner.NewRunnerFunc, opts ...PoolOption) *Pool {
	p := &Pool{
		factory:     factory,
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

// persistNewSession saves session metadata to the store and logs creation.
func (p *Pool) persistNewSession(info SessionInfo) (SessionInfo, error) {
	if p.store != nil {
		if err := p.store.SaveInfo(info); err != nil {
			return info, fmt.Errorf("persist session info: %w", err)
		}
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
	if p.store != nil {
		items, err := p.store.ListInfo(false)
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

	if p.store != nil {
		si, err := p.store.LoadInfo(sessionID)
		if err == nil {
			return si, nil
		}
	}
	return SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
}

// ListSessions returns metadata for all sessions.
func (p *Pool) ListSessions(includeArchived bool) ([]SessionInfo, error) {
	if p.store == nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		result := make([]SessionInfo, 0, len(p.sessions))
		for _, sess := range p.sessions {
			if !includeArchived && sess.Info.Archived {
				continue
			}
			result = append(result, sess.Info)
		}
		return result, nil
	}

	items, err := p.store.ListInfo(includeArchived)
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

	if p.store != nil {
		info, err := p.store.LoadInfo(sessionID)
		if err == nil {
			info.Archived = true
			if err := p.store.SaveInfo(info); err != nil {
				p.log.Warn("failed to persist archive", "session_id", sessionID, "error", err)
			}
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

// History returns the event log for a session, loading from disk if needed.
// Returns nil if the session has no history.
func (p *Pool) History(sessionID string) []runner.RPCEvent {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	if ok && len(sess.Events) > 0 {
		events := make([]runner.RPCEvent, len(sess.Events))
		copy(events, sess.Events)
		p.mu.Unlock()
		return events
	}
	p.mu.Unlock()

	if p.store != nil {
		events, err := p.store.Load(sessionID)
		if err == nil && len(events) > 0 {
			return events
		}
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
	p.log.Debug("chat started", "session_id", sessionID, "history_len", len(sess.Events), "message_len", len(msgText))

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
	p.mu.Unlock()
	p.touchLastActive(sessionID, now)

	// Store user message so stateless runners can reconstruct the conversation.
	userEvt := runner.UserMessageToRPCEvent(message)
	p.mu.Lock()
	sess.Events = append(sess.Events, userEvt)
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
		p.saveInfo(sess.Info)
	}
	p.mu.Unlock()
	p.persist(sessionID, userEvt)

	// Memory: ingest the user event.
	if p.mem != nil {
		if err := p.mem.Ingest(ctx, sessionID, userEvt); err != nil {
			p.log.Warn("memory ingest user event failed", "session_id", sessionID, "error", err)
		}
	}

	// Memory: assemble context within budget, falling back to in-memory events on error.
	events := sess.Events
	if p.mem != nil {
		assembled, err := p.mem.Assemble(ctx, sessionID, p.compaction.MaxTokens, p.compaction.KeepTail)
		if err != nil {
			p.log.Warn("memory assemble failed, falling back to session events",
				"session_id", sessionID, "error", err)
		} else {
			events = assembled
		}
	}

	stream := r.Chat(ctx, events, message)

	go func() {
		defer close(out)
		persistCtx := context.WithoutCancel(ctx)
		var textBuf strings.Builder
		for evt := range stream {
			if evt.Err != nil {
				// Persist any buffered text before returning on error.
				if textBuf.Len() > 0 {
					finalEvt := runner.AssistantMessageToRPCEvent(textBuf.String())
					p.persist(sessionID, finalEvt)
					if p.mem != nil {
						if err := p.mem.Ingest(persistCtx, sessionID, finalEvt); err != nil {
							p.log.Warn("memory ingest error-flush failed", "session_id", sessionID, "error", err)
						}
					}
				}
				out <- evt
				return
			}

			// Store events emitted by runners (tool calls, tool results, text deltas).
			if evt.Store != nil {
				// Flush buffered text before storing a non-text event.
				if textBuf.Len() > 0 {
					flushEvt := runner.AssistantMessageToRPCEvent(textBuf.String())
					p.persist(sessionID, flushEvt)
					if p.mem != nil {
						if err := p.mem.Ingest(persistCtx, sessionID, flushEvt); err != nil {
							p.log.Warn("memory ingest text-flush failed", "session_id", sessionID, "error", err)
						}
					}
					textBuf.Reset()
				}
				p.mu.Lock()
				sess.Events = append(sess.Events, *evt.Store)
				p.mu.Unlock()
				p.persist(sessionID, *evt.Store)
				if p.mem != nil {
					if err := p.mem.Ingest(persistCtx, sessionID, *evt.Store); err != nil {
						p.log.Warn("memory ingest store event failed", "session_id", sessionID, "error", err)
					}
				}
			}

			// Tool-use events pass through without history storage.
			if evt.ToolUse != nil {
				out <- evt
				continue
			}

			// Text delta: store in memory for the runner, buffer for persistence.
			if evt.Text != "" {
				rpcEvt := runner.TextDeltaToRPCEvent(evt.Text)
				p.mu.Lock()
				sess.Events = append(sess.Events, rpcEvt)
				p.mu.Unlock()
				textBuf.WriteString(evt.Text)
			}

			out <- evt
		}
		// Stream ended normally — persist the complete assistant message.
		if textBuf.Len() > 0 {
			finalEvt := runner.AssistantMessageToRPCEvent(textBuf.String())
			p.persist(sessionID, finalEvt)
			if p.mem != nil {
				if err := p.mem.Ingest(persistCtx, sessionID, finalEvt); err != nil {
					p.log.Warn("memory ingest final message failed", "session_id", sessionID, "error", err)
				}
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

	if p.mem != nil {
		if err := p.mem.Close(); err != nil {
			lastErr = err
		}
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

		// Restore metadata from index if available.
		if p.store != nil {
			if info, err := p.store.LoadInfo(sessionID); err == nil {
				sess.Info = SessionInfo(info)
			} else {
				sess.Info = SessionInfo{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now()}
			}
		} else {
			sess.Info = SessionInfo{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now()}
		}

		// Restore history from disk if available.
		if p.store != nil {
			p.mu.Unlock()
			events, err := p.store.Load(sessionID)
			p.mu.Lock()
			if err != nil {
				p.log.Warn("failed to load persisted session", "session_id", sessionID, "error", err)
			} else if len(events) > 0 {
				sess.Events = events
				p.log.Info("restored session from disk", "session_id", sessionID, "events", len(events))
			}
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
	if p.mem != nil {
		if err := p.mem.Bootstrap(context.Background(), sessionID); err != nil {
			p.log.Warn("memory bootstrap failed", "session_id", sessionID, "error", err)
		}
	}

	p.log.Info("created runner", "session_id", sessionID)
	return sess, r, nil
}

// persist appends events to the store if one is configured.
func (p *Pool) persist(sessionID string, events ...runner.RPCEvent) {
	if p.store == nil {
		return
	}
	if err := p.store.Append(sessionID, events...); err != nil {
		p.log.Warn("failed to persist event", "session_id", sessionID, "error", err)
	}
}

// saveInfo persists session metadata. Caller must hold p.mu.
func (p *Pool) saveInfo(info SessionInfo) {
	if p.store == nil {
		return
	}
	if err := p.store.SaveInfo(info); err != nil {
		p.log.Warn("failed to persist session info", "session_id", info.ID, "error", err)
	}
}

// touchLastActive updates the last active timestamp in the index.
func (p *Pool) touchLastActive(sessionID string, t time.Time) {
	if p.store == nil {
		return
	}
	info, err := p.store.LoadInfo(sessionID)
	if err != nil {
		return
	}
	info.LastActive = t
	if err := p.store.SaveInfo(info); err != nil {
		p.log.Warn("failed to update last active", "session_id", sessionID, "error", err)
	}
}
