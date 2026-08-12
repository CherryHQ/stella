package channel

import (
	"sync"
	"time"
)

type guestRateLimiter struct {
	mu        sync.Mutex
	windows   map[string]guestRateWindow
	now       func() time.Time
	lastSweep time.Time
}

type guestRateWindow struct {
	started time.Time
	count   int
}

func newGuestRateLimiter() *guestRateLimiter {
	return &guestRateLimiter{windows: make(map[string]guestRateWindow), now: time.Now}
}

func (l *guestRateLimiter) allow(guestID string, limit int) bool {
	if guestID == "" || limit < 1 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= time.Minute {
		for key, candidate := range l.windows {
			if now.Sub(candidate.started) >= 2*time.Minute {
				delete(l.windows, key)
			}
		}
		l.lastSweep = now
	}
	window := l.windows[guestID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = guestRateWindow{started: now}
	}
	if window.count >= limit {
		l.windows[guestID] = window
		return false
	}
	window.count++
	l.windows[guestID] = window
	return true
}
