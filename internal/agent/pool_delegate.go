package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
)

const delegateSessionChannel = "delegate"

// RunDelegateSession executes a delegate turn through Pool.Chat so the child
// transcript is persisted and can be resumed by passing the returned session_id.
func (p *Pool) RunDelegateSession(ctx context.Context, req delegatetool.SessionRunRequest) (delegatetool.SessionRunResult, error) {
	if strings.TrimSpace(req.Task) == "" {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, fmt.Errorf("delegate task is required")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	sessionID, err := p.ensureDelegateSession(ctx, req.SessionID)
	if err != nil {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, err
	}

	ctx = WithChannel(ctx, delegateSessionChannel)
	if req.System != "" {
		ctx = WithSystemOverride(ctx, req.System)
	}
	if len(req.ExcludedTools) > 0 {
		ctx = WithExcludedTools(ctx, req.ExcludedTools...)
	}

	stream := p.Chat(ctx, sessionID, req.Task, WithModel(req.Model))
	var output strings.Builder
	for ev := range stream {
		if ev.Text != "" {
			output.WriteString(ev.Text)
		}
		if ev.Err != nil {
			return delegatetool.SessionRunResult{SessionID: sessionID, Output: output.String()}, ev.Err
		}
	}
	return delegatetool.SessionRunResult{SessionID: sessionID, Output: output.String(), Complete: true}, nil
}

func (p *Pool) ensureDelegateSession(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return p.createDelegateSession(ctx)
	}

	p.mu.Lock()
	if sess, ok := p.sessions[sessionID]; ok {
		if sess.Info.Kind != "" && sess.Info.Kind != "delegate" {
			kind := sess.Info.Kind
			p.mu.Unlock()
			return sessionID, fmt.Errorf("session %q is %q, not a delegate session", sessionID, kind)
		}
		if sess.Info.Channel == "" {
			sess.Info.Channel = delegateSessionChannel
		}
		if sess.Info.Kind == "" {
			sess.Info.Kind = "delegate"
		}
		info := sess.Info
		p.mu.Unlock()
		if sm, ok := p.mem.(memory.SessionManager); ok {
			if err := sm.SaveInfo(p.sessionContext(ctx, info.UserID), info); err != nil {
				return sessionID, fmt.Errorf("persist delegate session info: %w", err)
			}
		}
		return sessionID, nil
	}
	p.mu.Unlock()

	if sm, ok := p.mem.(memory.SessionManager); ok {
		if info, err := sm.LoadInfo(ctx, sessionID); err == nil {
			return sessionID, p.persistLoadedDelegateSession(ctx, info)
		}
	}

	return p.createDelegateSessionWithID(ctx, sessionID)
}

func (p *Pool) createDelegateSession(ctx context.Context) (string, error) {
	p.mu.Lock()
	info := p.createSessionLocked(delegateSessionChannel, memory.UserIDFromContext(ctx))
	info.Kind = "delegate"
	info.ProjectID = p.parentProjectIDLocked(memory.SessionIDFromContext(ctx))
	if sess, ok := p.sessions[info.ID]; ok {
		sess.Info = info
	}
	p.mu.Unlock()
	if _, err := p.persistNewSession(ctx, info); err != nil {
		return info.ID, err
	}
	return info.ID, nil
}

func (p *Pool) createDelegateSessionWithID(ctx context.Context, sessionID string) (string, error) {
	now := time.Now()
	info := SessionInfo{
		ID:         sessionID,
		Channel:    delegateSessionChannel,
		Kind:       "delegate",
		CreatedAt:  now,
		LastActive: now,
		AgentID:    p.agentID,
		UserID:     memory.UserIDFromContext(ctx),
		ProjectID:  "",
	}
	p.mu.Lock()
	info.ProjectID = p.parentProjectIDLocked(memory.SessionIDFromContext(ctx))
	p.sessions[sessionID] = &Session{Info: info}
	p.mu.Unlock()
	if _, err := p.persistNewSession(ctx, info); err != nil {
		return sessionID, err
	}
	return sessionID, nil
}

func (p *Pool) persistLoadedDelegateSession(ctx context.Context, info SessionInfo) error {
	if info.Kind != "" && info.Kind != "delegate" {
		return fmt.Errorf("session %q is %q, not a delegate session", info.ID, info.Kind)
	}
	changed := false
	if info.Channel == "" {
		info.Channel = delegateSessionChannel
		changed = true
	}
	if info.Kind == "" {
		info.Kind = "delegate"
		changed = true
	}
	p.mu.Lock()
	p.sessions[info.ID] = &Session{Info: info}
	p.mu.Unlock()
	if changed {
		if sm, ok := p.mem.(memory.SessionManager); ok {
			return sm.SaveInfo(p.sessionContext(ctx, info.UserID), info)
		}
	}
	return nil
}

func (p *Pool) parentProjectIDLocked(parentSessionID string) string {
	if parentSessionID == "" {
		return ""
	}
	if parent, ok := p.sessions[parentSessionID]; ok {
		return parent.Info.ProjectID
	}
	return ""
}
