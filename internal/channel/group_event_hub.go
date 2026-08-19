package channel

import (
	"sync"

	"github.com/CherryHQ/stella/internal/eventlog"
)

// GroupEventHub provides best-effort in-process notifications. The event log
// remains canonical; slow subscribers are dropped and must reconnect by seq.
// Single replica only. Use PG LISTEN/NOTIFY when multi-replica is needed.
type GroupEventHub struct {
	mu   sync.Mutex
	next uint64
	subs map[string]map[uint64]chan GroupEvent
}

// GroupTurnEvent is an ephemeral execution projection. Message rows remain the
// canonical replay source; reconnecting clients intentionally do not replay it.
type GroupTurnEvent struct {
	AgentID string `json:"agent_id"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// GroupEvent carries either a canonical message projection or a live turn
// state. Embedding keeps existing message subscribers source-compatible.
type GroupEvent struct {
	eventlog.AppendResult
	Turn *GroupTurnEvent
}

func NewGroupEventHub() *GroupEventHub {
	return &GroupEventHub{subs: make(map[string]map[uint64]chan GroupEvent)}
}

func (h *GroupEventHub) Announce(event eventlog.AppendResult) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast(event.GroupID, GroupEvent{AppendResult: event})
}

// AnnounceTurn publishes a non-durable execution state after the dispatcher
// has made its durable transition. It never substitutes for message replay.
func (h *GroupEventHub) AnnounceTurn(groupID, agentID, state, reason string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast(groupID, GroupEvent{Turn: &GroupTurnEvent{AgentID: agentID, State: state, Reason: reason}})
}

func (h *GroupEventHub) broadcast(groupID string, event GroupEvent) {
	for id, ch := range h.subs[groupID] {
		select {
		case ch <- event:
		default:
			// Turn events carry no AppendResult, so the group id must come from
			// the parameter: deleting by event.GroupID leaves the closed channel
			// registered and the next broadcast sends on it.
			close(ch)
			delete(h.subs[groupID], id)
		}
	}
}

func (h *GroupEventHub) Subscribe(groupID string) (<-chan GroupEvent, func()) {
	ch := make(chan GroupEvent, 16)
	if h == nil {
		close(ch)
		return ch, func() {}
	}
	h.mu.Lock()
	h.next++
	id := h.next
	if h.subs[groupID] == nil {
		h.subs[groupID] = make(map[uint64]chan GroupEvent)
	}
	h.subs[groupID][id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if current, ok := h.subs[groupID][id]; ok {
			delete(h.subs[groupID], id)
			close(current)
		}
	}
}
