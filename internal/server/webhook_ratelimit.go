package server

import (
	"math"
	"sync"
	"time"
)

// defaultWebhookMaxInflight caps concurrent runs per webhook. A run can
// outlive its request by minutes (up to max_run_timeout), so an acceptance
// rate alone would let a leaked capability stack up hundreds of background runs.
// Ceiling: raise (or make configurable) if a legit fan-out workload appears.
const defaultWebhookMaxInflight = 10

// defaultWebhookMaxIngress caps concurrent in-flight *pre-admission* requests
// per endpoint after candidate resolution: the window that reads the bounded
// body and admits. It is distinct from both the acceptance token bucket (a rate) and
// the run slot (a whole run's lifetime); a slow or oversized body holds only an
// ingress slot, never an acceptance token or a run slot. The slot is short-lived
// (bounded by the body-read deadline plus synchronous admission), so a small
// ceiling suffices. Upgrade trigger: raise it (or make it per-endpoint
// configurable) only if a legitimate burst of concurrent slow-body deliveries to
// one endpoint is observed being rejected.
const defaultWebhookMaxIngress = 10

// webhookLimiter throttles webhook ingress per instance on three independent
// axes: a token bucket caps the acceptance rate, an ingress counter caps
// concurrent pre-admission (body+admit) requests, and an in-flight
// counter caps concurrent runs. Unlike the login RateLimiter (which counts
// *failed* attempts to slow brute force), this throttles *accepted* webhook
// calls. It is in-memory and per-process: with multiple replicas each pod
// enforces its own budget (documented limitation, acceptable for a rate cap).
type webhookLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*tokenBucket
	ingress     map[string]int
	inflight    map[string]int
	rate        float64 // tokens refilled per second
	burst       float64 // bucket capacity
	maxIngress  int
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
		ingress:     make(map[string]int),
		inflight:    make(map[string]int),
		rate:        ratePerSec,
		burst:       burst,
		maxIngress:  defaultWebhookMaxIngress,
		maxInflight: defaultWebhookMaxInflight,
		now:         time.Now,
	}
}

// allow consumes one token for key, returning false when the bucket is empty.
// Delete calls remove so user-created webhook churn cannot grow this map forever.
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

// acquireIngress reserves a pre-admission ingress slot for key. A successful
// reservation must be paired with exactly one releaseIngress once the
// body/admit window completes (success, error, timeout, or cancel).
// It consumes neither an acceptance token nor a run slot.
func (l *webhookLimiter) acquireIngress(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ingress[key] >= l.maxIngress {
		return false
	}
	l.ingress[key]++
	return true
}

// releaseIngress releases a pre-admission ingress slot for key.
func (l *webhookLimiter) releaseIngress(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ingress[key] > 1 {
		l.ingress[key]--
		return
	}
	delete(l.ingress, key)
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

// remove forgets all process-local limits for a deleted, non-reusable webhook
// UUID. Releases from requests or runs that were already in flight become safe
// no-ops; the deleted UUID can never identify a new resource.
func (l *webhookLimiter) remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
	delete(l.ingress, key)
	delete(l.inflight, key)
}
