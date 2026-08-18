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
	subs map[string]map[uint64]chan eventlog.AppendResult
}

func NewGroupEventHub() *GroupEventHub {
	return &GroupEventHub{subs: make(map[string]map[uint64]chan eventlog.AppendResult)}
}

func (h *GroupEventHub) Announce(event eventlog.AppendResult) {
	if h == nil || !event.Inserted {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs[event.GroupID] {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(h.subs[event.GroupID], id)
		}
	}
}

func (h *GroupEventHub) Subscribe(groupID string) (<-chan eventlog.AppendResult, func()) {
	ch := make(chan eventlog.AppendResult, 16)
	if h == nil {
		close(ch)
		return ch, func() {}
	}
	h.mu.Lock()
	h.next++
	id := h.next
	if h.subs[groupID] == nil {
		h.subs[groupID] = make(map[uint64]chan eventlog.AppendResult)
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
