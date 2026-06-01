package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- Regression tests for patched CR-007: wrong-kind resume rejected ---

func TestEnsure_SchedulerCannotResumeDelegateSession(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["del-1"] = NewInfo("del-1", "agent1", "u1", "delegate", KindDelegate, "", now)

	_, err := r.Ensure(context.Background(), Request{
		ID:          "del-1",
		UserID:      "u1",
		RequireKind: KindScheduler,
	})
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("scheduler resuming delegate session: expected ErrWrongKind, got %v", err)
	}
}

func TestEnsure_DelegateCannotResumeSchedulerSession(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["sched-1"] = NewInfo("sched-1", "agent1", "u1", "scheduler", KindScheduler, "", now)

	_, err := r.Ensure(context.Background(), Request{
		ID:          "sched-1",
		UserID:      "u1",
		RequireKind: KindDelegate,
	})
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("delegate resuming scheduler session: expected ErrWrongKind, got %v", err)
	}
}

func TestEnsure_ChatCannotResumeTaskSession(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["task-1"] = NewInfo("task-1", "agent1", "u1", "task", KindTask, "", now)

	_, err := r.Ensure(context.Background(), Request{
		ID:          "task-1",
		UserID:      "u1",
		RequireKind: KindChat,
	})
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("chat resuming task session: expected ErrWrongKind, got %v", err)
	}
}

// --- Trusted exact-create paths: channel/scheduler system-derived IDs ---

func TestEnsure_TrustedExactCreateWithKindValidation(t *testing.T) {
	r, _ := newTestRegistry(t)

	// Create with trusted system key + RequireKind.
	info, err := r.Ensure(context.Background(), Request{
		ID:                 "channel:telegram:group:123",
		UserID:             "u1",
		Kind:               KindChat,
		Channel:            ChannelTelegram,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        KindChat,
	})
	if err != nil {
		t.Fatalf("trusted exact-create: %v", err)
	}
	if info.ID != "channel:telegram:group:123" {
		t.Errorf("ID = %q, want channel:telegram:group:123", info.ID)
	}
	if Kind(info.Kind) != KindChat {
		t.Errorf("kind = %q, want chat", info.Kind)
	}
}

func TestEnsure_TrustedExactCreateResumeRejectsWrongKind(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	// Pre-existing delegate session with the same ID as a channel key (unlikely but tests the guard).
	s.sessions["channel:key:1"] = NewInfo("channel:key:1", "agent1", "u1", "delegate", KindDelegate, "", now)

	_, err := r.Ensure(context.Background(), Request{
		ID:                 "channel:key:1",
		UserID:             "u1",
		Kind:               KindChat,
		Channel:            ChannelTelegram,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        KindChat,
	})
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("trusted exact-create collision: expected ErrWrongKind, got %v", err)
	}
}

// --- Resume-only: unknown ID without AllowExactIDCreate must not create ---

func TestEnsure_ResumeOnlyUnknownIDDoesNotCreate(t *testing.T) {
	r, s := newTestRegistry(t)

	_, err := r.Ensure(context.Background(), Request{
		ID:              "nonexistent-session",
		UserID:          "u1",
		Kind:            KindChat,
		CreateIfMissing: true,
		// AllowExactIDCreate NOT set => resume-only
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("resume-only unknown ID: expected ErrNotFound, got %v", err)
	}
	if _, exists := s.sessions["nonexistent-session"]; exists {
		t.Error("resume-only path must not create a session row")
	}
}
