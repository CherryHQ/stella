package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/memory"
)

// CreateSession creates a new session with a generated ID and persists its metadata.
func (p *Pool) CreateSession(channel string, userID ...int64) (SessionInfo, error) {
	p.mu.Lock()
	info := p.createSessionLocked(channel, userID...)
	p.mu.Unlock()
	return p.persistNewSession(info)
}

// createSessionLocked creates a new session and adds it to the in-memory map.
// Caller must hold p.mu.
func (p *Pool) createSessionLocked(channel string, userID ...int64) SessionInfo {
	now := time.Now()
	info := SessionInfo{
		ID:         channel + "-" + uuid.New().String()[:8],
		Channel:    channel,
		CreatedAt:  now,
		LastActive: now,
		AgentID:    p.agentID,
	}
	if len(userID) > 0 && userID[0] != 0 {
		info.UserID = userID[0]
	}
	p.sessions[info.ID] = &Session{Info: info}
	return info
}

// persistNewSession saves session metadata to the memory provider and logs creation.
func (p *Pool) persistNewSession(info SessionInfo) (SessionInfo, error) {
	if sm, ok := p.mem.(memory.SessionManager); ok {
		if err := sm.SaveInfo(context.Background(), info); err != nil {
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
	if sm, ok := p.mem.(memory.SessionManager); ok {
		items, err := sm.ListInfo(context.Background(), memory.ListOptions{})
		if err == nil {
			for _, info := range items {
				if info.Channel != channel {
					continue
				}
				if _, inSeen := seen[info.ID]; inSeen {
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
// An optional userID associates the session with a user (stored in conversations table).
func (p *Pool) ResolveSession(channel string, userID ...int64) (SessionInfo, error) {
	p.mu.Lock()
	if info, ok := p.activeSessionLocked(channel); ok {
		p.mu.Unlock()
		return info, nil
	}
	info := p.createSessionLocked(channel, userID...)
	p.mu.Unlock()
	return p.persistNewSession(info)
}

// RotateSession archives the active session for a channel (if any) and creates a new one.
func (p *Pool) RotateSession(channel string, userID ...int64) (SessionInfo, error) {
	if old, ok := p.ActiveSession(channel); ok {
		if err := p.ArchiveSession(old.ID); err != nil {
			p.log.Warn("archive failed during rotate", "session_id", old.ID, "error", err)
		}
	}
	return p.CreateSession(channel, userID...)
}

// GetSession returns metadata for a session.
func (p *Pool) GetSession(sessionID string) (SessionInfo, error) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	p.mu.Unlock()
	if ok {
		return sess.Info, nil
	}

	if sm, ok := p.mem.(memory.SessionManager); ok {
		si, err := sm.LoadInfo(context.Background(), sessionID)
		if err == nil {
			return si, nil
		}
	}
	return SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
}

// ListSessions returns metadata for all sessions.
func (p *Pool) ListSessions(includeArchived bool) ([]SessionInfo, error) {
	sm, ok := p.mem.(memory.SessionManager)
	if !ok {
		return nil, nil
	}
	items, err := sm.ListInfo(context.Background(), memory.ListOptions{IncludeArchived: includeArchived})
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
	var r Runner
	if ok {
		r = sess.Runner
		delete(p.sessions, sessionID)
	}
	p.mu.Unlock()

	if sm, ok := p.mem.(memory.SessionManager); ok {
		info, err := sm.LoadInfo(context.Background(), sessionID)
		if err == nil {
			info.Archived = true
			if err := sm.SaveInfo(context.Background(), info); err != nil {
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

// History returns the message history for a session, loading from the memory provider.
// Returns nil if the session has no history or the provider does not support it.
func (p *Pool) History(sessionID string) []ai.Message {
	sm, ok := p.mem.(memory.SessionManager)
	if !ok {
		return nil
	}
	msgs, err := sm.LoadHistory(context.Background(), sessionID)
	if err == nil && len(msgs) > 0 {
		return msgs
	}
	return nil
}
