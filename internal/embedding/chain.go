package embedding

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Chain is an API-first provider chain: it tries providers in priority order
// (primary first) and falls back to the next only on transient failure. The
// rules, in order, for each request:
//
//   - Privacy: a PrivacySensitive request skips every KindAPI provider.
//   - Circuit breaker: a provider whose breaker is open is skipped without a
//     call, so a dead API costs one timeout per open-window, not one per request.
//   - Terminal error: a caller/data fault (IsTerminal) stops the chain and
//     returns immediately — falling back would only get the same rejection.
//   - Transient error: records a breaker failure and advances to the next
//     provider.
//
// If no provider is eligible (all skipped) the chain returns ErrNoProvider; if
// every eligible provider failed transiently it returns the last error.
type Chain struct {
	providers []Provider
	breakers  []*breaker
}

// BreakerConfig tunes the per-provider circuit breaker. Zero values fall back to
// sensible defaults (see NewChain).
type BreakerConfig struct {
	// FailureThreshold is the count of consecutive transient failures that trips
	// the breaker open.
	FailureThreshold int
	// OpenDuration is how long the breaker stays open before a single half-open
	// probe is allowed through.
	OpenDuration time.Duration
}

// NewChain builds a chain over providers in priority order (primary first). A
// nil clock uses time.Now; tests inject a fake one.
func NewChain(providers []Provider, cfg BreakerConfig, clock func() time.Time) *Chain {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = 60 * time.Second
	}
	if clock == nil {
		clock = time.Now
	}
	breakers := make([]*breaker, len(providers))
	for i := range providers {
		breakers[i] = &breaker{threshold: cfg.FailureThreshold, openFor: cfg.OpenDuration, now: clock}
	}
	return &Chain{providers: providers, breakers: breakers}
}

// Embed runs the request through the chain and returns the first success.
func (c *Chain) Embed(ctx context.Context, req Request) (Result, error) {
	var lastErr error
	eligible := false

	for i, p := range c.providers {
		if req.Privacy == PrivacySensitive && p.Kind() == KindAPI {
			continue
		}
		if !c.breakers[i].allow() {
			continue
		}
		eligible = true

		res, err := p.Embed(ctx, req)
		// The caller no longer needs a result. Do not turn its cancellation into a
		// provider failure (or send it to a fallback): an upstream timeout remains
		// a transient provider failure as long as this context is still live.
		if err := ctx.Err(); err != nil {
			c.breakers[i].releaseProbe()
			return Result{}, err
		}
		if err == nil {
			c.breakers[i].recordSuccess()
			res.ProviderName = p.Name()
			res.Model = p.Model()
			res.FallbackUsed = i > 0
			return res, nil
		}
		// Caller/data faults are not the provider's fault to fix: every provider
		// would reject identically, so stop rather than fan the same error out.
		if IsTerminal(err) {
			c.breakers[i].releaseProbe()
			return Result{}, err
		}
		c.breakers[i].recordFailure()
		lastErr = err
	}

	if !eligible {
		return Result{}, ErrNoProvider
	}
	return Result{}, fmt.Errorf("embedding: all providers failed: %w", lastErr)
}

// breaker is a per-provider circuit breaker. It is closed normally, opens after
// `threshold` consecutive transient failures, and after `openFor` admits exactly
// one half-open probe whose outcome closes (success) or re-opens (failure) it.
type breaker struct {
	threshold int
	openFor   time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time // zero when closed
	probing   bool      // a half-open probe is in flight
}

// allow reports whether a call may proceed, claiming the single half-open probe
// slot when the open window has elapsed.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true // closed
	}
	if b.now().Before(b.openUntil) {
		return false // still open
	}
	if b.probing {
		return false // someone else holds the probe slot
	}
	b.probing = true
	return true
}

func (b *breaker) recordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.probing = false
}

func (b *breaker) recordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probing {
		// Half-open probe failed: re-open for another full window.
		b.probing = false
		b.openUntil = b.now().Add(b.openFor)
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.openFor)
	}
}

// releaseProbe gives back a half-open slot when the caller cancels. Cancellation
// is not a provider outcome, so it neither closes nor extends the breaker window.
func (b *breaker) releaseProbe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}
