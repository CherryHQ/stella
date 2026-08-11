package oauth

import (
	"sync"
	"time"
)

// FlowStore is an in-memory store of in-flight device-flow sessions.
// Known limitation: a process restart loses all pending flows.
type FlowStore struct {
	mu    sync.Mutex
	flows map[string]FlowStatus
}

// NewFlowStore returns an empty FlowStore.
func NewFlowStore() *FlowStore {
	return &FlowStore{flows: make(map[string]FlowStatus)}
}

// Create stores a new FlowStatus keyed by its FlowID.
func (s *FlowStore) Create(status FlowStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[status.FlowID] = status
}

// CreateExclusive stores status only when no live flow exists for the same
// user/provider. The check and insert share one lock.
func (s *FlowStore) CreateExclusive(status FlowStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, flow := range s.flows {
		if flow.UserID == status.UserID && flow.Provider == status.Provider && flow.State == FlowStatePending && now.Before(flow.ExpiresAt) {
			return false
		}
	}
	s.flows[status.FlowID] = status
	return true
}

// Get returns the FlowStatus for flowID, or false if not found.
func (s *FlowStore) Get(flowID string) (FlowStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, ok := s.flows[flowID]
	return fs, ok
}

// Update sets state on the named flow and then calls update (if non-nil) to
// allow further mutation. The state is applied first so callers can inspect or
// override it inside update.
func (s *FlowStore) Update(flowID string, state FlowState, update func(*FlowStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, ok := s.flows[flowID]
	if !ok {
		return
	}
	fs.State = state
	if update != nil {
		update(&fs)
	}
	s.flows[flowID] = fs
}

// Delete removes the flow with the given ID from the store.
func (s *FlowStore) Delete(flowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.flows, flowID)
}
