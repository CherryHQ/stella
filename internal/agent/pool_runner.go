package agent

import (
	"context"
	"io"
	"time"

	"github.com/CherryHQ/stella/pkg/memory"
)

// getOrCreateRunner returns the session and its runner, creating both if needed.
// If the session is not in memory but exists on disk, its history is restored.
// If model is non-empty and differs from the session's current model, the
// existing runner is replaced.
func (p *Pool) getOrCreateRunner(ctx context.Context, sessionID string, model string) (*Session, Runner, error) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionID]
	if ok && sess.Runner != nil {
		// Check if the runner is still alive (for runners that support liveness).
		if aliver, isAliver := sess.Runner.(Aliver); isAliver && !aliver.Alive() {
			p.log.Warn("replacing dead runner", "session_id", sessionID)
			if closer, isCloser := sess.Runner.(io.Closer); isCloser {
				_ = closer.Close()
			}
			sess.Runner = nil
		}
	}
	if ok && sess.Runner != nil {
		// If a specific model was requested and it differs from the session's
		// current model, replace the
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

		// Restore metadata from memory provider if available.
		if sm, ok := p.mem.(memory.SessionManager); ok {
			if info, err := sm.LoadInfo(context.Background(), sessionID); err == nil {
				sess.Info = info
			} else {
				sess.Info = SessionInfo{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now()}
			}
		} else {
			sess.Info = SessionInfo{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now()}
		}
	}
	if sess.Info.UserID == 0 {
		if userID := memory.UserIDFromContext(ctx); userID != 0 {
			sess.Info.UserID = userID
		}
	}
	if sess.Info.AgentID == "" {
		if agentID := memory.AgentIDFromContext(ctx); agentID != "" {
			sess.Info.AgentID = agentID
		} else if p.agentID != "" {
			sess.Info.AgentID = p.agentID
		}
	}

	// Snapshot all mutable pool fields while still holding the lock.
	factory := p.factory
	hooksFn := p.hooksFn
	defaultModel := p.defaultModel
	p.mu.Unlock()

	// Resolve the model: explicit > session's current > pool default.
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = sess.Model
	}
	if effectiveModel == "" {
		effectiveModel = defaultModel
	}

	r, err := factory(ctx, RunnerParams{Model: effectiveModel, Memory: p.mem, UserID: sess.Info.UserID, AgentID: p.agentID, HooksFn: hooksFn})
	if err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	// Re-check: another goroutine may have installed a runner while we were unlocked.
	if sess.Runner != nil {
		p.mu.Unlock()
		// Close the runner we just created — the other goroutine's runner wins.
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
		return sess, sess.Runner, nil
	}
	sess.Runner = r
	sess.Model = effectiveModel
	p.mu.Unlock()

	// Memory: bootstrap the conversation for this session.
	memSession := memory.Session{ID: sessionID, AgentID: p.agentID, UserID: sess.Info.UserID}
	if err := p.mem.Bootstrap(context.Background(), memSession); err != nil {
		p.log.Warn("memory bootstrap failed", "session_id", sessionID, "error", err)
	}

	p.log.Info("created runner", "session_id", sessionID)
	return sess, r, nil
}
