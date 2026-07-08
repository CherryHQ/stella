package server

import (
	"math"
	"sync"
	"time"
)

// webhookLimiter is a per-webhook token-bucket rate limiter. Unlike the login
// RateLimiter (which counts *failed* attempts to slow brute force), this
// throttles *accepted* webhook calls so a leaked PAT cannot saturate agent
// concurrency. It is in-memory and per-process: with multiple replicas each pod
// enforces its own budget (documented limitation, acceptable for a rate cap).
type webhookLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens refilled per second
	burst   float64 // bucket capacity
	now     func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newWebhookLimiter(ratePerSec, burst float64) *webhookLimiter {
	return &webhookLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    ratePerSec,
		burst:   burst,
		now:     time.Now,
	}
}

// allow consumes one token for key, returning false when the bucket is empty.
// The webhook set is small and admin-created, so buckets are never evicted.
func (l *webhookLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
