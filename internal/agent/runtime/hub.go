package runtime

import (
	"encoding/json"
	"sync"

	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// SessionHub fans out a session's live turn events to any number of read-only
// subscribers. The runtime publishes every event of an in-flight turn; HTTP SSE
// handlers subscribe to watch a turn after navigation or connection loss, one
// driven server-side, or one started from another browser tab.
//
// Publishing never blocks the turn: a slow subscriber drops events rather than
// stalling the agent. While a turn is active, the hub retains a bounded replay
// so a newly attached subscriber can reconstruct output emitted before it
// connected. Subscribers still reconcile final state from persisted history.
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
	mu     sync.Mutex
	subs   map[string]map[chan Event]struct{}
	live   map[string]int // session ID → in-flight turn count
	replay map[string]*replayState
}

type replayState struct {
	events   []Event
	bytes    int
	disabled bool
}

const (
	// subBuffer bounds live per-subscriber buffering before events are dropped.
	subBuffer = 256
	// Replay is deliberately process-local: it covers browser navigation and
	// transient disconnects. A durable event log is the upgrade when turns must
	// survive process replacement. The byte ceiling prevents tool/image output
	// from turning one active session into unbounded server memory.
	replayMaxEvents = 4096
	replayMaxBytes  = 8 << 20
)

// NewSessionHub returns an empty hub.
func NewSessionHub() *SessionHub {
	return &SessionHub{
		subs:   make(map[string]map[chan Event]struct{}),
		live:   make(map[string]int),
		replay: make(map[string]*replayState),
	}
}

// Subscribe registers a listener for a session's live events. Events already
// emitted by the active turn are queued before live delivery. The returned
// channel closes when the turn ends or the caller invokes cancel.
func (h *SessionHub) Subscribe(sessionID string) (<-chan Event, func()) {
	h.mu.Lock()
	replayed := h.replay[sessionID]
	capacity := subBuffer
	if replayed != nil && !replayed.disabled && len(replayed.events)+subBuffer > capacity {
		capacity = len(replayed.events) + subBuffer
	}
	ch := make(chan Event, capacity)
	if replayed != nil && !replayed.disabled {
		for _, event := range replayed.events {
			ch <- event
		}
	}
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
	if h.live[sessionID] == 0 {
		h.replay[sessionID] = &replayState{}
	}
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
		delete(h.replay, sessionID)
		for ch := range h.subs[sessionID] {
			close(ch)
		}
		delete(h.subs, sessionID)
	}
	h.mu.Unlock()
}

// publish records and delivers an event without blocking the producer.
func (h *SessionHub) publish(sessionID string, event Event) {
	// Store events are never encoded by the SSE boundary, so retaining them only
	// burns replay memory. Size everything else before taking the runtime-wide
	// hub lock; structured tool arguments are rare but may require JSON encoding.
	replayable := event.Store == nil
	size := 0
	deltaKind := 0
	if replayable {
		size = replayEventSize(event)
		deltaKind = replayDeltaKind(event)
	}

	h.mu.Lock()
	if state := h.replay[sessionID]; replayable && state != nil && !state.disabled {
		coalescesWithTail := deltaKind != 0 && len(state.events) > 0 &&
			replayDeltaKind(state.events[len(state.events)-1]) == deltaKind
		deltaSize := len(event.Text) + len(event.Reasoning)
		switch {
		case coalescesWithTail && state.bytes+deltaSize > replayMaxBytes:
			disableReplay(state)
		case coalescesWithTail:
			last := &state.events[len(state.events)-1]
			last.Text += event.Text
			last.Reasoning += event.Reasoning
			state.bytes += deltaSize
		case len(state.events) >= replayMaxEvents || state.bytes+size > replayMaxBytes:
			disableReplay(state)
		default:
			state.events = append(state.events, event)
			state.bytes += size
		}
	}
	for ch := range h.subs[sessionID] {
		select {
		case ch <- event:
		default:
		}
	}
	h.mu.Unlock()
}

func disableReplay(state *replayState) {
	state.events = nil
	state.bytes = 0
	state.disabled = true
}

func replayDeltaKind(event Event) int {
	if event.Image != nil || event.File != nil || event.ToolUse != nil || len(event.References) > 0 || event.Step != nil || event.Store != nil || event.Err != nil {
		return 0
	}
	if event.Text != "" && event.Reasoning == "" {
		return 1
	}
	if event.Reasoning != "" && event.Text == "" {
		return 2
	}
	return 0
}

func replayEventSize(event Event) int {
	size := 64 + len(event.Text) + len(event.Reasoning)
	if event.Image != nil {
		size += len(event.Image.Data) + len(event.Image.MimeType)
	}
	if event.File != nil {
		size += len(event.File.Path) + len(event.File.Name)
	}
	if event.ToolUse != nil {
		size += len(event.ToolUse.ID) + len(event.ToolUse.Tool) + len(event.ToolUse.Status) + len(event.ToolUse.Input) + len(event.ToolUse.Detail) + len(event.ToolUse.Content)
		if arguments, err := json.Marshal(event.ToolUse.Arguments); err == nil {
			size += len(arguments)
		} else {
			return replayMaxBytes + 1
		}
		size += replayReferencesSize(event.ToolUse.References)
	}
	size += replayReferencesSize(event.References)
	if event.Err != nil {
		size += len(event.Err.Error())
	}
	return size
}

func replayReferencesSize(references []renderrefs.Reference) int {
	size := 0
	for _, reference := range references {
		size += 64 + len(reference.Type) + len(reference.ID) + len(reference.Intent) + len(reference.AgentID)
		if reference.Preview != nil {
			size += len(reference.Preview.Title) + len(reference.Preview.Status)
		}
	}
	return size
}
