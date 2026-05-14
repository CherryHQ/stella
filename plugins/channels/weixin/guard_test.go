package weixin

import (
	"errors"
	"testing"
	"time"
)

func TestSessionGuard_NotPausedByDefault(t *testing.T) {
	t.Parallel()
	var g SessionGuard
	if g.IsPaused() {
		t.Error("new guard should not be paused")
	}
	if err := g.AssertActive(); err != nil {
		t.Errorf("AssertActive on new guard: %v", err)
	}
}

func TestSessionGuard_PauseBlocksSends(t *testing.T) {
	t.Parallel()
	var g SessionGuard
	g.Pause(time.Hour)
	if !g.IsPaused() {
		t.Error("IsPaused should be true immediately after Pause")
	}
	err := g.AssertActive()
	if err == nil {
		t.Fatal("AssertActive should return error while paused")
	}
	if !errors.Is(err, ErrSessionPaused) {
		t.Errorf("error should wrap ErrSessionPaused, got: %v", err)
	}
}

func TestSessionGuard_ExpiresAfterDuration(t *testing.T) {
	t.Parallel()
	var g SessionGuard
	g.Pause(10 * time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	if g.IsPaused() {
		t.Error("IsPaused should be false after expiry")
	}
	if err := g.AssertActive(); err != nil {
		t.Errorf("AssertActive should return nil after expiry, got: %v", err)
	}
}

func TestSessionGuard_ResetExtendsPause(t *testing.T) {
	t.Parallel()
	var g SessionGuard
	g.Pause(10 * time.Millisecond)

	// Re-pause before expiry — should extend window.
	time.Sleep(5 * time.Millisecond)
	g.Pause(time.Hour)

	if !g.IsPaused() {
		t.Error("guard should still be paused after re-pause")
	}
}

func TestSessionGuard_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	var g SessionGuard

	done := make(chan struct{})
	go func() {
		for range 100 {
			g.Pause(time.Millisecond)
		}
		close(done)
	}()

	for range 100 {
		_ = g.IsPaused()
		_ = g.AssertActive()
	}
	<-done
}
