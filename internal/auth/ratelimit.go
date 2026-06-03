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
	cleanupInterval     = time.Minute
)

// Rate limiting errors.
var (
	ErrRateLimitIP           = errors.New("too many requests from this IP, try again later")
	ErrRateLimitUsername     = errors.New("too many failed attempts for this account, try again in 30 seconds")
	ErrRateLimitRegistration = errors.New("too many registration attempts for this email, try again in 30 seconds")
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
	ips           sync.Map // string -> *ipRecord
	usernames     sync.Map // string -> *usernameRecord
	registrations sync.Map // string -> *usernameRecord

	cleanupMu sync.Mutex
	cleanupAt time.Time
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{}
}

// CheckIP verifies the IP has not exceeded the request limit.
// Does not increment the counter — call RecordIPAttempt after a failed attempt.
func (rl *RateLimiter) CheckIP(ip string) error {
	now := time.Now()
	rl.cleanupExpired(now)

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
	rl.cleanupExpired(now)

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
	return rl.checkName(&rl.usernames, username, ErrRateLimitUsername)
}

// RecordLoginFailure records a failed login attempt for a username.
func (rl *RateLimiter) RecordLoginFailure(username string) {
	rl.recordNameFailure(&rl.usernames, username)
}

// RecordLoginSuccess resets the failure counter for a username.
func (rl *RateLimiter) RecordLoginSuccess(username string) {
	rl.usernames.Delete(username)
}

// CheckRegistration verifies the email is not in a registration cooldown period.
func (rl *RateLimiter) CheckRegistration(email string) error {
	return rl.checkName(&rl.registrations, email, ErrRateLimitRegistration)
}

// RecordRegistrationFailure records a failed registration attempt for an email.
func (rl *RateLimiter) RecordRegistrationFailure(email string) {
	rl.recordNameFailure(&rl.registrations, email)
}

// RecordRegistrationSuccess resets the registration failure counter for an email.
func (rl *RateLimiter) RecordRegistrationSuccess(email string) {
	rl.registrations.Delete(email)
}

func (rl *RateLimiter) checkName(records *sync.Map, name string, limitErr error) error {
	now := time.Now()
	rl.cleanupExpired(now)

	val, ok := records.Load(name)
	if !ok {
		return nil
	}

	rec := val.(*usernameRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.failures >= usernameMaxFailures {
		if now.Sub(rec.lastFailure) < usernameCooldown {
			return limitErr
		}
		// Cooldown expired, reset.
		rec.failures = 0
	}

	return nil
}

func (rl *RateLimiter) recordNameFailure(records *sync.Map, name string) {
	now := time.Now()
	rl.cleanupExpired(now)

	val, _ := records.LoadOrStore(name, &usernameRecord{})
	rec := val.(*usernameRecord)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	// Reset if cooldown has passed.
	if rec.failures >= usernameMaxFailures && now.Sub(rec.lastFailure) >= usernameCooldown {
		rec.failures = 0
	}

	rec.failures++
	rec.lastFailure = now
}

func (rl *RateLimiter) cleanupExpired(now time.Time) {
	rl.cleanupMu.Lock()
	defer rl.cleanupMu.Unlock()
	if now.Sub(rl.cleanupAt) < cleanupInterval {
		return
	}
	rl.cleanupAt = now

	rl.ips.Range(func(key, value any) bool {
		rec := value.(*ipRecord)
		rec.mu.Lock()
		expired := now.Sub(rec.windowAt) > ipWindowDuration
		rec.mu.Unlock()
		if expired {
			rl.ips.Delete(key)
		}
		return true
	})
	cleanupNameRecords(&rl.usernames, now)
	cleanupNameRecords(&rl.registrations, now)
}

func cleanupNameRecords(records *sync.Map, now time.Time) {
	records.Range(func(key, value any) bool {
		rec := value.(*usernameRecord)
		rec.mu.Lock()
		expired := rec.lastFailure.IsZero() || now.Sub(rec.lastFailure) > usernameCooldown
		rec.mu.Unlock()
		if expired {
			records.Delete(key)
		}
		return true
	})
}
