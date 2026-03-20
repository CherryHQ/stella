package auth

import (
	"errors"
	"sync"
	"time"
)

// Rate limiting constants.
const (
	ipMaxAttempts       = 10
	ipWindowDuration    = time.Minute
	usernameMaxFailures = 5
	usernameCooldown    = 30 * time.Second
)

// Rate limiting errors.
var (
	ErrRateLimitIP       = errors.New("too many requests from this IP, try again later")
	ErrRateLimitUsername = errors.New("too many failed attempts for this account, try again in 30 seconds")
)

// ipRecord tracks login attempts per IP address.
type ipRecord struct {
	mu       sync.Mutex
	attempts int
	windowAt time.Time
}

// usernameRecord tracks consecutive login failures per username.
type usernameRecord struct {
	mu          sync.Mutex
	failures    int
	lastFailure time.Time
}

// RateLimiter provides per-IP and per-username rate limiting for login attempts.
type RateLimiter struct {
	ips       sync.Map // string -> *ipRecord
	usernames sync.Map // string -> *usernameRecord
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{}
}

// CheckIP verifies the IP has not exceeded the request limit.
// Does not increment the counter — call RecordIPAttempt after a failed attempt.
func (rl *RateLimiter) CheckIP(ip string) error {
	now := time.Now()

	val, ok := rl.ips.Load(ip)
	if !ok {
		return nil
	}

	rec := val.(*ipRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	// Reset window if expired.
	if now.Sub(rec.windowAt) > ipWindowDuration {
		rec.attempts = 0
		rec.windowAt = now
		return nil
	}

	if rec.attempts >= ipMaxAttempts {
		return ErrRateLimitIP
	}

	return nil
}

// RecordIPAttempt records a failed attempt for rate limiting by IP.
func (rl *RateLimiter) RecordIPAttempt(ip string) {
	now := time.Now()

	val, _ := rl.ips.LoadOrStore(ip, &ipRecord{windowAt: now})
	rec := val.(*ipRecord)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if now.Sub(rec.windowAt) > ipWindowDuration {
		rec.attempts = 0
		rec.windowAt = now
	}

	rec.attempts++
}

// CheckUsername verifies the username is not in a cooldown period.
func (rl *RateLimiter) CheckUsername(username string) error {
	val, ok := rl.usernames.Load(username)
	if !ok {
		return nil
	}

	rec := val.(*usernameRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.failures >= usernameMaxFailures {
		if time.Since(rec.lastFailure) < usernameCooldown {
			return ErrRateLimitUsername
		}
		// Cooldown expired, reset.
		rec.failures = 0
	}

	return nil
}

// RecordLoginFailure records a failed login attempt for a username.
func (rl *RateLimiter) RecordLoginFailure(username string) {
	now := time.Now()

	val, _ := rl.usernames.LoadOrStore(username, &usernameRecord{})
	rec := val.(*usernameRecord)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	// Reset if cooldown has passed.
	if rec.failures >= usernameMaxFailures && time.Since(rec.lastFailure) >= usernameCooldown {
		rec.failures = 0
	}

	rec.failures++
	rec.lastFailure = now
}

// RecordLoginSuccess resets the failure counter for a username.
func (rl *RateLimiter) RecordLoginSuccess(username string) {
	rl.usernames.Delete(username)
}
