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

// CreateExclusive stores status as the only pending flow for its user/provider.
// A previous pending flow is superseded; a flow already completing cannot be
// replaced because its token exchange may have succeeded. The check and insert
// share one lock.
func (s *FlowStore) CreateExclusive(status FlowStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for flowID, flow := range s.flows {
		live := (flow.State == FlowStatePending && now.Before(flow.ExpiresAt)) || flow.State == FlowStateCompleting
		if flow.UserID == status.UserID && flow.Provider == status.Provider && live {
			if flow.State == FlowStateCompleting {
				return false
			}
			delete(s.flows, flowID)
		}
	}
	s.flows[status.FlowID] = status
	return true
}

// Claim atomically moves a live pending flow into completing state. It rejects
// expired and replayed callbacks before any token exchange occurs.
func (s *FlowStore) Claim(flowID string) (FlowStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.flows[flowID]
	if !ok || flow.State != FlowStatePending {
		return FlowStatus{}, false
	}
	if !time.Now().Before(flow.ExpiresAt) {
		flow.State = FlowStateExpired
		s.flows[flowID] = flow
		return FlowStatus{}, false
	}
	flow.State = FlowStateCompleting
	s.flows[flowID] = flow
	return flow, true
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
