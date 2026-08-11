package oauth

import (
	"testing"
	"time"
)

func TestFlowStore_CreateAndGet(t *testing.T) {
	s := NewFlowStore()
	flow := FlowStatus{
		Provider:        ProviderGitHub,
		FlowID:          "flow-1",
		VerificationURI: "https://github.com/login/device",
		UserCode:        "ABCD-1234",
		ExpiresAt:       time.Now().Add(15 * time.Minute),
		State:           FlowStatePending,
	}

	s.Create(flow)

	got, ok := s.Get("flow-1")
	if !ok {
		t.Fatal("Get: expected flow to exist")
	}
	if got.FlowID != flow.FlowID {
		t.Errorf("FlowID = %q, want %q", got.FlowID, flow.FlowID)
	}
	if got.State != FlowStatePending {
		t.Errorf("State = %q, want %q", got.State, FlowStatePending)
	}
	if got.VerificationURI != flow.VerificationURI {
		t.Errorf("VerificationURI = %q, want %q", got.VerificationURI, flow.VerificationURI)
	}
}

func TestFlowStore_GetMissing(t *testing.T) {
	s := NewFlowStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("Get: expected false for missing flow")
	}
}

func TestFlowStore_Update(t *testing.T) {
	s := NewFlowStore()
	s.Create(FlowStatus{
		FlowID: "flow-2",
		State:  FlowStatePending,
	})

	s.Update("flow-2", FlowStateAuthorized, nil)

	got, ok := s.Get("flow-2")
	if !ok {
		t.Fatal("Get after Update: expected flow to exist")
	}
	if got.State != FlowStateAuthorized {
		t.Errorf("State = %q, want %q", got.State, FlowStateAuthorized)
	}
}

func TestFlowStore_UpdateWithMutator(t *testing.T) {
	s := NewFlowStore()
	s.Create(FlowStatus{
		FlowID: "flow-3",
		State:  FlowStatePending,
	})

	sentinel := "verified-code"
	s.Update("flow-3", FlowStateAuthorized, func(fs *FlowStatus) {
		fs.UserCode = sentinel
	})

	got, ok := s.Get("flow-3")
	if !ok {
		t.Fatal("Get: expected flow to exist")
	}
	if got.State != FlowStateAuthorized {
		t.Errorf("State = %q, want %q", got.State, FlowStateAuthorized)
	}
	if got.UserCode != sentinel {
		t.Errorf("UserCode = %q, want %q", got.UserCode, sentinel)
	}
}

func TestFlowStore_UpdateMissingIsNoOp(t *testing.T) {
	s := NewFlowStore()
	// Should not panic.
	s.Update("no-such-flow", FlowStateFailed, nil)
}

func TestFlowStore_Delete(t *testing.T) {
	s := NewFlowStore()
	s.Create(FlowStatus{FlowID: "flow-del", State: FlowStatePending})

	s.Delete("flow-del")

	_, ok := s.Get("flow-del")
	if ok {
		t.Fatal("Get after Delete: expected flow to be absent")
	}
}

func TestFlowStore_DeleteMissingIsNoOp(t *testing.T) {
	s := NewFlowStore()
	// Should not panic.
	s.Delete("not-there")
}

func TestFlowStoreCreateExclusiveRejectsConcurrentUserProviderFlow(t *testing.T) {
	s := NewFlowStore()
	first := FlowStatus{
		FlowID: "first", UserID: "user-1", Provider: ProviderGitHub,
		State: FlowStatePending, ExpiresAt: time.Now().Add(time.Minute),
	}
	if !s.CreateExclusive(first) {
		t.Fatal("first CreateExclusive rejected")
	}
	second := first
	second.FlowID = "second"
	if s.CreateExclusive(second) {
		t.Fatal("second concurrent CreateExclusive succeeded")
	}
	second.UserID = "user-2"
	if !s.CreateExclusive(second) {
		t.Fatal("different user should have an independent flow")
	}
}

func TestFlowStoreClaimRejectsReplayAndExpiredFlow(t *testing.T) {
	s := NewFlowStore()
	s.Create(FlowStatus{FlowID: "live", State: FlowStatePending, ExpiresAt: time.Now().Add(time.Minute)})
	claimed, ok := s.Claim("live")
	if !ok || claimed.State != FlowStateCompleting {
		t.Fatalf("Claim(live) = (%+v, %v), want completing", claimed, ok)
	}
	if _, ok := s.Claim("live"); ok {
		t.Fatal("replayed Claim(live) succeeded")
	}

	s.Create(FlowStatus{FlowID: "expired", State: FlowStatePending, ExpiresAt: time.Now().Add(-time.Second)})
	if _, ok := s.Claim("expired"); ok {
		t.Fatal("Claim(expired) succeeded")
	}
	got, _ := s.Get("expired")
	if got.State != FlowStateExpired {
		t.Fatalf("expired state = %q, want %q", got.State, FlowStateExpired)
	}
}
