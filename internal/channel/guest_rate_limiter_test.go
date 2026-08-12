package channel

import (
	"testing"
	"time"
)

func TestGuestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newGuestRateLimiter()
	limiter.now = func() time.Time { return now }

	for range 2 {
		if !limiter.allow("guest-1", 2) {
			t.Fatal("configured guest budget was rejected")
		}
	}
	if limiter.allow("guest-1", 2) {
		t.Fatal("message above the configured guest budget was accepted")
	}
	if !limiter.allow("guest-2", 2) {
		t.Fatal("one guest exhausted another guest's budget")
	}
	now = now.Add(time.Minute)
	if !limiter.allow("guest-1", 2) {
		t.Fatal("guest budget did not reset after one minute")
	}
}
