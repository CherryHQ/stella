package agent

import (
	"context"
	"io"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
)

// StartReaper runs a background goroutine that periodically checks for
// idle or dead runners. It returns when ctx is cancelled.
func (p *Pool) StartReaper(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reap()
		}
	}
}

// reap checks all sessions and closes runners that are idle or dead.
func (p *Pool) reap() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for id, sess := range p.sessions {
		if sess.Runner == nil {
			continue
		}

		aliver, isAliver := sess.Runner.(runner.Aliver)
		tracker, isTracker := sess.Runner.(runner.ActivityTracker)

		if !isAliver {
			continue
		}

		if !aliver.Alive() {
			p.log.Warn("removing dead runner", "session_id", id)
			if closer, isCloser := sess.Runner.(io.Closer); isCloser {
				_ = closer.Close()
			}
			sess.Runner = nil
			continue
		}

		if isTracker && now.Sub(tracker.LastActivity()) > p.idleTimeout {
			p.log.Info("reaping idle runner", "session_id", id, "idle_duration", now.Sub(tracker.LastActivity()).Round(time.Second))
			if closer, isCloser := sess.Runner.(io.Closer); isCloser {
				_ = closer.Close()
			}
			sess.Runner = nil
		}
	}
}
