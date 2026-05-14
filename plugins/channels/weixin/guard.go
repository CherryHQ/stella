package weixin

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrSessionPaused is returned by AssertActive when the guard is in a pause window.
var ErrSessionPaused = errors.New("weixin: session paused (ret=-14 expiry window)")

// SessionGuard tracks a timed pause triggered by a session-expiry response (ret=-14).
// The official plugin pauses all outbound sends for 1h after a session expiry, then
// resumes. The polling loop is not gated — only send paths check AssertActive.
//
// All methods are safe for concurrent use.
type SessionGuard struct {
	mu      sync.Mutex
	pauseAt time.Time // zero = not paused
	until   time.Time
}

// Pause activates the guard for the given duration from now.
// Calling Pause while already paused resets the window.
func (g *SessionGuard) Pause(d time.Duration) {
	now := time.Now()
	g.mu.Lock()
	g.pauseAt = now
	g.until = now.Add(d)
	g.mu.Unlock()
}

// IsPaused reports whether the guard is currently active.
func (g *SessionGuard) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.pauseAt.IsZero() && time.Now().Before(g.until)
}

// AssertActive returns ErrSessionPaused if the guard is active, nil otherwise.
func (g *SessionGuard) AssertActive() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pauseAt.IsZero() || !time.Now().Before(g.until) {
		return nil
	}
	remaining := time.Until(g.until).Round(time.Second)
	return fmt.Errorf("%w: %v remaining", ErrSessionPaused, remaining)
}
