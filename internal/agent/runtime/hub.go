package runtime

import "sync"

// SessionHub is a wake-only signal: it tells a durable-log reader on this
// replica that new events for the session were committed, so the reader does
// not wait out its full poll interval. It carries no events and advances no
// cursor — ctx_session_event is the only truth a reader may consume.
type SessionHub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
	live map[string]int // session ID → in-flight turn count
}

// NewSessionHub returns an empty hub.
func NewSessionHub() *SessionHub {
	return &SessionHub{
		subs: make(map[string]map[chan struct{}]struct{}),
		live: make(map[string]int),
	}
}

// Watch registers a wake listener for a session. The channel receives at most
// one coalesced wake per commit batch; it closes when the session's last
// in-flight turn ends or the caller invokes the returned cancel func.
func (h *SessionHub) Watch(sessionID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	set := h.subs[sessionID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		h.subs[sessionID] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[sessionID]; set != nil {
				if _, ok := set[ch]; ok {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(h.subs, sessionID)
				}
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// IsLive reports whether a turn is currently in flight on the session.
func (h *SessionHub) IsLive(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[sessionID] > 0
}

// begin marks a turn as in flight.
func (h *SessionHub) begin(sessionID string) {
	h.mu.Lock()
	h.live[sessionID]++
	h.mu.Unlock()
}

// end clears a turn. The last turn's end wakes every watcher once more so a
// reader notices the terminal marker without waiting out its poll.
func (h *SessionHub) end(sessionID string) {
	h.mu.Lock()
	if h.live[sessionID] > 0 {
		h.live[sessionID]--
	}
	if h.live[sessionID] == 0 {
		delete(h.live, sessionID)
		for ch := range h.subs[sessionID] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	h.mu.Unlock()
}

// wake signals a committed event batch. It never blocks the producer.
func (h *SessionHub) wake(sessionID string) {
	h.mu.Lock()
	for ch := range h.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}
