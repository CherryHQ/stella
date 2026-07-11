package server

import (
	"math"
	"sync"
	"time"
)

// defaultWebhookMaxInflight caps concurrent runs per webhook. A run can
// outlive its request by minutes (up to max_run_timeout), so an acceptance
// rate alone would let a leaked PAT stack up hundreds of background runs.
// Ceiling: raise (or make configurable) if a legit fan-out workload appears.
const defaultWebhookMaxInflight = 10

// webhookLimiter throttles webhook ingress per instance on two axes: a token
// bucket caps the acceptance rate, and an in-flight counter caps concurrent
// runs. Unlike the login RateLimiter (which counts *failed* attempts to slow
// brute force), this throttles *accepted* webhook calls. It is in-memory and
// per-process: with multiple replicas each pod enforces its own budget
// (documented limitation, acceptable for a rate cap).
type webhookLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*tokenBucket
	inflight    map[string]int
	rate        float64 // tokens refilled per second
	burst       float64 // bucket capacity
	maxInflight int
	now         func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newWebhookLimiter(ratePerSec, burst float64) *webhookLimiter {
	return &webhookLimiter{
		buckets:     make(map[string]*tokenBucket),
		inflight:    make(map[string]int),
		rate:        ratePerSec,
		burst:       burst,
		maxInflight: defaultWebhookMaxInflight,
		now:         time.Now,
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

// beginRun reserves an in-flight run slot for key. The caller must pair a
// successful reservation with exactly one endRun once the run terminates.
func (l *webhookLimiter) beginRun(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] >= l.maxInflight {
		return false
	}
	l.inflight[key]++
	return true
}

// endRun releases an in-flight run slot for key.
func (l *webhookLimiter) endRun(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] > 1 {
		l.inflight[key]--
		return
	}
	delete(l.inflight, key)
}
