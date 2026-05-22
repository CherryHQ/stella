package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// CreateSession creates a new session with a generated ID and persists its metadata.
func (p *Pool) CreateSession(channel, userID string) (SessionInfo, error) {
	return p.CreateSessionWithKind(channel, "chat", "", userID)
}

// CreateSessionWithKind creates a new session with explicit kind and projectID.
func (p *Pool) CreateSessionWithKind(channel, kind, projectID, userID string) (SessionInfo, error) {
	if kind == "" {
		kind = "chat"
	}
	p.mu.Lock()
	info := p.createSessionLocked(channel, userID)
	info.Kind = kind
	info.ProjectID = projectID
	if sess, ok := p.sessions[info.ID]; ok {
		sess.Info.Kind = kind
		sess.Info.ProjectID = projectID
	}
	p.mu.Unlock()
	return p.persistNewSession(context.Background(), info)
}

// createSessionLocked creates a new session and adds it to the in-memory map.
// Caller must hold p.mu.
func (p *Pool) createSessionLocked(channel, userID string) SessionInfo {
	now := time.Now()
	info := SessionInfo{
		ID:         uuid.New().String(),
		Channel:    channel,
		CreatedAt:  now,
		LastActive: now,
		AgentID:    p.agentID,
		UserID:     userID,
	}
	p.sessions[info.ID] = &Session{Info: info}
	return info
}

// persistNewSession saves session metadata to the memory provider and logs creation.
func (p *Pool) persistNewSession(ctx context.Context, info SessionInfo) (SessionInfo, error) {
	if sm, ok := p.mem.(memory.SessionManager); ok {
		ctx = p.sessionContext(ctx, info.UserID)
		if err := sm.SaveInfo(ctx, info); err != nil {
			return info, fmt.Errorf("persist session info: %w", err)
		}
	}
	p.log.Info("session created", "session_id", info.ID, "channel", info.Channel)
	return info, nil
}

func (p *Pool) sessionContext(ctx context.Context, userID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if userID != "" && memory.UserIDFromContext(ctx) == "" {
		ctx = memory.WithUserID(ctx, userID)
	}
	if p.agentID != "" && memory.AgentIDFromContext(ctx) == "" {
		ctx = memory.WithAgentID(ctx, p.agentID)
	}
	return ctx
}

func isPrivateMainChannel(channel string, userID string) bool {
	return userID != "" && strings.Contains(channel, ":user:")
}

func mainCandidate(info SessionInfo, userID string) bool {
	if info.Archived || info.UserID != userID || info.ProjectID != "" {
		return false
	}
	if info.Kind == "task" || info.Kind == "scheduler" {
		return false
	}
	return isPrivateMainChannel(info.Channel, userID)
}

// activeSessionLocked returns the most recent non-archived session for a channel
// from the in-memory map and persistent store. Caller must hold p.mu.
func (p *Pool) activeSessionLocked(ctx context.Context, channel string, userID string) (SessionInfo, bool) {
	var best *SessionInfo
	seen := make(map[string]struct{})

	for _, sess := range p.sessions {
		if sess.Info.Archived || sess.Info.Channel != channel {
			continue
		}
		if userID != "" && sess.Info.UserID != userID {
			continue
		}
		seen[sess.Info.ID] = struct{}{}
		if best == nil || sess.Info.LastActive.After(best.LastActive) {
			info := sess.Info
			best = &info
		}
	}

	if sm, ok := p.mem.(memory.SessionManager); ok {
		items, err := sm.ListInfo(p.sessionContext(ctx, userID), memory.ListOptions{AgentID: p.agentID, UserID: userID})
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

// mainSessionLocked returns the non-archived main session from the in-memory
// map and persistent store. Caller must hold p.mu.
func (p *Pool) mainSessionLocked(ctx context.Context, userID string) (SessionInfo, bool) {
	for _, sess := range p.sessions {
		if sess.Info.Archived || sess.Info.Kind != "main" || sess.Info.UserID != userID {
			continue
		}
		return sess.Info, true
	}
	if sm, ok := p.mem.(memory.SessionManager); ok {
		items, err := sm.ListInfo(p.sessionContext(ctx, userID), memory.ListOptions{Kind: "main", AgentID: p.agentID, UserID: userID})
		if err == nil {
			for _, info := range items {
				return info, true
			}
		}
	}
	return SessionInfo{}, false
}

// latestSessionLocked returns the most recent non-archived private session for a user.
// Caller must hold p.mu.
func (p *Pool) latestSessionLocked(ctx context.Context, userID string) (SessionInfo, bool) {
	var best *SessionInfo
	seen := make(map[string]struct{})
	for _, sess := range p.sessions {
		if !mainCandidate(sess.Info, userID) {
			continue
		}
		seen[sess.Info.ID] = struct{}{}
		if best == nil || sess.Info.LastActive.After(best.LastActive) {
			info := sess.Info
			best = &info
		}
	}
	if sm, ok := p.mem.(memory.SessionManager); ok {
		items, err := sm.ListInfo(p.sessionContext(ctx, userID), memory.ListOptions{AgentID: p.agentID, UserID: userID})
		if err == nil {
			for _, info := range items {
				if _, inSeen := seen[info.ID]; inSeen || !mainCandidate(info, userID) {
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

// ResolveSession returns the session for a channel, creating or promoting one if needed.
// Private user channels resolve to a per-agent/user main session. Group and other
// shared channels resolve by exact channel and remain chat sessions.
func (p *Pool) ResolveSession(ctx context.Context, channel, userID string) (SessionInfo, error) {
	ctx = p.sessionContext(ctx, userID)
	privateMain := isPrivateMainChannel(channel, userID)

	p.mu.Lock()

	if privateMain {
		if info, ok := p.mainSessionLocked(ctx, userID); ok {
			p.mu.Unlock()
			return info, nil
		}

		if info, ok := p.latestSessionLocked(ctx, userID); ok {
			info.Kind = "main"
			if sess, exists := p.sessions[info.ID]; exists {
				sess.Info.Kind = "main"
			}
			ensurer := p.projectEnsurer
			p.mu.Unlock()
			if ensurer != nil && info.UserID != "" {
				if _, err := ensurer(ctx, p.agentID, info.UserID); err != nil {
					p.log.Warn("project ensurer failed", "agent_id", p.agentID, "user_id", info.UserID, "error", err)
				}
			}
			return p.persistNewSession(ctx, info)
		}
	} else if info, ok := p.activeSessionLocked(ctx, channel, userID); ok {
		p.mu.Unlock()
		return info, nil
	}

	info := p.createSessionLocked(channel, userID)
	if privateMain {
		info.Kind = "main"
	} else {
		info.Kind = "chat"
	}
	if sess, exists := p.sessions[info.ID]; exists {
		sess.Info.Kind = info.Kind
	}
	ensurer := p.projectEnsurer
	p.mu.Unlock()

	if privateMain && ensurer != nil && info.UserID != "" {
		if _, err := ensurer(ctx, p.agentID, info.UserID); err != nil {
			p.log.Warn("project ensurer failed", "agent_id", p.agentID, "user_id", info.UserID, "error", err)
		}
	}

	return p.persistNewSession(ctx, info)
}

// GetSession returns metadata for a session.
func (p *Pool) GetSession(sessionID, userID string) (SessionInfo, error) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	p.mu.Unlock()
	if ok {
		return sess.Info, nil
	}

	if sm, ok := p.mem.(memory.SessionManager); ok {
		si, err := sm.LoadInfo(p.sessionContext(context.Background(), userID), sessionID)
		if err == nil {
			return si, nil
		}
	}
	return SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
}

// ListSessions returns metadata for all sessions.
func (p *Pool) ListSessions(includeArchived bool, userID string) ([]SessionInfo, error) {
	sm, ok := p.mem.(memory.SessionManager)
	if !ok {
		return nil, nil
	}
	items, err := sm.ListInfo(context.Background(), memory.ListOptions{UserID: userID, AgentID: p.agentID, IncludeArchived: includeArchived})
	if err != nil {
		return nil, err
	}
	result := make([]SessionInfo, len(items))
	copy(result, items)
	return result, nil
}

// History returns the message history for a session, loading from the memory provider.
// Returns nil if the session has no history or the provider does not support it.
func (p *Pool) History(ctx context.Context, sessionID string) []ai.Message {
	sm, ok := p.mem.(memory.SessionManager)
	if !ok {
		return nil
	}
	msgs, err := sm.LoadHistory(ctx, sessionID)
	if err == nil && len(msgs) > 0 {
		return msgs
	}
	return nil
}
