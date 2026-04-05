package agent

import (
	"context"
	"io"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
)

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

	// Load per-user memory for system prompt injection.
	var userMem string
	if p.userMemory != nil && sess.Info.UserID != 0 && p.agentID != "" {
		if content, err := p.userMemory.Get(ctx, sess.Info.UserID, p.agentID); err == nil {
			userMem = content
		} else {
			p.log.Warn("failed to load user memory", "user_id", sess.Info.UserID, "agent_id", p.agentID, "error", err)
		}
	}

	r, err := p.factory(ctx, runner.RunnerParams{Model: effectiveModel, UserMemory: userMem, UserID: sess.Info.UserID})
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
