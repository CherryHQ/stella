package agent

import (
	"context"
	"time"
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

		if sess.Runner.Busy() {
			continue
		}

		if !sess.Runner.Alive() {
			p.log.Warn("removing dead runner", "session_id", id)
			_ = sess.Runner.Close()
			sess.Runner = nil
			continue
		}

		if now.Sub(sess.Runner.LastActivity()) > p.idleTimeout {
			p.log.Info("reaping idle runner", "session_id", id, "idle_duration", now.Sub(sess.Runner.LastActivity()).Round(time.Second))
			_ = sess.Runner.Close()
			sess.Runner = nil
		}
	}
}
