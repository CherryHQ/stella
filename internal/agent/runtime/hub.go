package runtime

import "sync"

// SessionHub fans out a session's live turn events to any number of read-only
// subscribers. The runtime publishes every event of an in-flight turn; HTTP SSE
// handlers subscribe to watch a turn they did not initiate — a scheduler/task
// turn driven server-side, or a turn started from another browser tab.
//
// Publishing never blocks the turn: a slow subscriber drops events rather than
// stalling the agent. Subscribers reconcile final state by reloading persisted
// history, so dropped deltas are cosmetic.
//
// Placement invariant: the hub lives on the Runtime that executes a session's
// turns, and the SSE handler subscribes via the Service of `session.AgentID`.
// This is correct only because every turn for a session — chat, scheduler,
// task, and delegate — runs on that agent's Runtime. If a turn ever runs on a
// different Runtime (e.g. a cross-agent delegate, or a standalone task runner),
// it would publish to a hub the watcher never subscribes to and the live
// stream would silently fall back to 204. Such a change must hoist the hub to a
// single per-pool instance keyed by session ID.
type SessionHub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
	live map[string]int // session ID → in-flight turn count
}

// subBuffer bounds per-subscriber buffering before events are dropped.
const subBuffer = 256

// NewSessionHub returns an empty hub.
func NewSessionHub() *SessionHub {
	return &SessionHub{
		subs: make(map[string]map[chan Event]struct{}),
		live: make(map[string]int),
	}
}

// Subscribe registers a listener for a session's live events. The returned
// channel delivers events until the turn ends (then it is closed) or the caller
// invokes cancel. Callers must always invoke cancel to avoid leaking the
// subscription.
func (h *SessionHub) Subscribe(sessionID string) (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	h.mu.Lock()
	set := h.subs[sessionID]
	if set == nil {
		set = make(map[chan Event]struct{})
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

// end clears a turn. When the last turn on a session finishes, all of its
// subscriber channels are closed so SSE handlers can finish their streams.
func (h *SessionHub) end(sessionID string) {
	h.mu.Lock()
	if h.live[sessionID] > 0 {
		h.live[sessionID]--
	}
	if h.live[sessionID] == 0 {
		delete(h.live, sessionID)
		for ch := range h.subs[sessionID] {
			close(ch)
		}
		delete(h.subs, sessionID)
	}
	h.mu.Unlock()
}

// publish delivers an event to every current subscriber, dropping it for any
// subscriber whose buffer is full.
func (h *SessionHub) publish(sessionID string, ev Event) {
	h.mu.Lock()
	for ch := range h.subs[sessionID] {
		select {
		case ch <- ev:
		default:
		}
	}
	h.mu.Unlock()
}
