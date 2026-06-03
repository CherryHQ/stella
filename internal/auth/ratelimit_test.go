package auth_test

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestRateLimiterIPBasic(t *testing.T) {
	rl := auth.NewRateLimiter()

	// CheckIP alone should never block (no attempts recorded yet).
	if err := rl.CheckIP("1.2.3.4"); err != nil {
		t.Fatalf("fresh IP should not be limited: %v", err)
	}

	// Record 10 failed attempts.
	for range 10 {
		rl.RecordIPAttempt("1.2.3.4")
	}

	// 11th check should fail.
	if err := rl.CheckIP("1.2.3.4"); !errors.Is(err, auth.ErrRateLimitIP) {
		t.Errorf("expected ErrRateLimitIP, got %v", err)
	}

	// Different IP should still work.
	if err := rl.CheckIP("5.6.7.8"); err != nil {
		t.Fatalf("different IP should not be rate limited: %v", err)
	}
}

func TestRateLimiterIPSuccessDoesNotCount(t *testing.T) {
	rl := auth.NewRateLimiter()

	// CheckIP many times without RecordIPAttempt should never block.
	for range 20 {
		if err := rl.CheckIP("1.2.3.4"); err != nil {
			t.Fatalf("successful requests should not be counted: %v", err)
		}
	}
}

func TestRateLimiterUsernameFailures(t *testing.T) {
	rl := auth.NewRateLimiter()

	// No failures yet, should pass.
	if err := rl.CheckUsername("testuser"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Record 5 failures.
	for range 5 {
		rl.RecordLoginFailure("testuser")
	}

	// Should be rate limited.
	if err := rl.CheckUsername("testuser"); !errors.Is(err, auth.ErrRateLimitUsername) {
		t.Errorf("expected ErrRateLimitUsername, got %v", err)
	}

	// Different username should work.
	if err := rl.CheckUsername("otheruser"); err != nil {
		t.Fatalf("different user should not be rate limited: %v", err)
	}
}

func TestRateLimiterLoginSuccess(t *testing.T) {
	rl := auth.NewRateLimiter()

	// Record 4 failures.
	for range 4 {
		rl.RecordLoginFailure("testuser")
	}

	// Success should reset.
	rl.RecordLoginSuccess("testuser")

	// Should be able to fail again.
	for range 5 {
		rl.RecordLoginFailure("testuser")
	}

	if err := rl.CheckUsername("testuser"); !errors.Is(err, auth.ErrRateLimitUsername) {
		t.Errorf("expected ErrRateLimitUsername after new failures, got %v", err)
	}
}

func TestRateLimiterBelowThreshold(t *testing.T) {
	rl := auth.NewRateLimiter()

	// 4 failures is below threshold.
	for range 4 {
		rl.RecordLoginFailure("testuser")
	}

	if err := rl.CheckUsername("testuser"); err != nil {
		t.Errorf("4 failures should not trigger rate limit, got %v", err)
	}
}

func TestRateLimiterRegistrationFailuresDoNotBlockLogin(t *testing.T) {
	rl := auth.NewRateLimiter()

	for range 5 {
		rl.RecordRegistrationFailure("user@example.com")
	}
	if err := rl.CheckRegistration("user@example.com"); !errors.Is(err, auth.ErrRateLimitRegistration) {
		t.Errorf("expected ErrRateLimitRegistration, got %v", err)
	}
	if err := rl.CheckUsername("user@example.com"); err != nil {
		t.Errorf("registration failures should not block login, got %v", err)
	}
}
