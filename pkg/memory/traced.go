package memory

import (
	"context"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
)

// Unwrap returns the innermost Provider by recursively unwrapping traced layers.
// If p does not implement the unwrapper interface, p itself is returned.
func Unwrap(p Provider) Provider {
	type unwrapper interface {
		Unwrap() Provider
	}
	for {
		u, ok := p.(unwrapper)
		if !ok {
			return p
		}
		p = u.Unwrap()
	}
}

// WithTracing wraps a Provider to emit PostMemoryCall hooks after each operation.
// hooksFn is called on each operation to get the current HookSet; it may return nil.
// The returned Provider implements all optional capability interfaces (Compactor,
// Searcher, Explorer, ProfileStore, SessionManager, ReviewSource). Methods for
// capabilities not supported by the inner provider return sensible zero values or errors.
// Use [Unwrap] to check the inner provider's actual capabilities.
func WithTracing(provider Provider, hooksFn func() *hooks.HookSet) Provider {
	return &tracedProvider{
		inner:   provider,
		hooksFn: hooksFn,
	}
}

// tracedProvider wraps a Provider and emits PostMemoryCall hooks.
type tracedProvider struct {
	inner   Provider
	hooksFn func() *hooks.HookSet
}

// Unwrap returns the inner Provider for capability detection.
func (t *tracedProvider) Unwrap() Provider { return t.inner }

func (t *tracedProvider) hooks() *hooks.HookSet {
	if t.hooksFn == nil {
		return nil
	}
	return t.hooksFn()
}

func (t *tracedProvider) emit(ctx context.Context, hctx *hooks.PostMemoryCallContext) {
	hs := t.hooks()
	if hs == nil || hs.Empty() {
		return
	}
	hs.RunPostMemoryCall(ctx, hctx)
}

// ---------------------------------------------------------------------------
// Provider (core interface)
// ---------------------------------------------------------------------------

func (t *tracedProvider) Name() string { return t.inner.Name() }

func (t *tracedProvider) Bootstrap(ctx context.Context, session Session) error {
	start := time.Now()
	err := t.inner.Bootstrap(ctx, session)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:        hooks.MemoryOpBootstrap,
		SessionID: session.ID,
		Duration:  time.Since(start),
		Error:     err,
	})
	return err
}

func (t *tracedProvider) Append(ctx context.Context, session Session, msgs ...ai.Message) error {
	start := time.Now()
	err := t.inner.Append(ctx, session, msgs...)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:           hooks.MemoryOpAppend,
		SessionID:    session.ID,
		Duration:     time.Since(start),
		Error:        err,
		MessageCount: len(msgs),
	})
	return err
}

func (t *tracedProvider) Assemble(ctx context.Context, session Session, budget, freshTail int) ([]ai.Message, error) {
	start := time.Now()
	msgs, err := t.inner.Assemble(ctx, session, budget, freshTail)
	// Estimate tokens in the assembled result.
	var tokens int
	for _, m := range msgs {
		tokens += EstimateTokens(MessageText(m))
	}
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:           hooks.MemoryOpAssemble,
		SessionID:    session.ID,
		Duration:     time.Since(start),
		Error:        err,
		MessageCount: len(msgs),
		TokenCount:   tokens,
	})
	return msgs, err
}

func (t *tracedProvider) Stats(ctx context.Context, session Session) (SessionStats, error) {
	start := time.Now()
	stats, err := t.inner.Stats(ctx, session)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:           hooks.MemoryOpStats,
		SessionID:    session.ID,
		Duration:     time.Since(start),
		Error:        err,
		TokenCount:   stats.TokenCount,
		SummaryCount: stats.SummaryCount,
	})
	return stats, err
}

func (t *tracedProvider) Close() error { return t.inner.Close() }

// ---------------------------------------------------------------------------
// Compactor
// ---------------------------------------------------------------------------

func (t *tracedProvider) NeedsCompaction(ctx context.Context, session Session, threshold float64) bool {
	c, ok := t.inner.(Compactor)
	if !ok {
		return false
	}
	return c.NeedsCompaction(ctx, session, threshold)
}

func (t *tracedProvider) Compact(ctx context.Context, session Session, mode CompactionMode) (*CompactionResult, error) {
	c, ok := t.inner.(Compactor)
	if !ok {
		return nil, errCapabilityNotSupported("Compactor")
	}
	start := time.Now()
	result, err := c.Compact(ctx, session, mode)
	hctx := &hooks.PostMemoryCallContext{
		Op:        hooks.MemoryOpCompact,
		SessionID: session.ID,
		Duration:  time.Since(start),
		Error:     err,
	}
	if result != nil {
		hctx.SummaryCount = result.LeafSummariesCreated + result.CondensedSummariesCreated
		hctx.TokenCount = result.TokensAfter
		hctx.TokenDelta = result.TokensAfter - result.TokensBefore
	}
	t.emit(ctx, hctx)
	return result, err
}

// ---------------------------------------------------------------------------
// Searcher
// ---------------------------------------------------------------------------

func (t *tracedProvider) Search(ctx context.Context, session Session, query SearchQuery) ([]SearchResult, error) {
	s, ok := t.inner.(Searcher)
	if !ok {
		return nil, errCapabilityNotSupported("Searcher")
	}
	start := time.Now()
	results, err := s.Search(ctx, session, query)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:          hooks.MemoryOpSearch,
		SessionID:   session.ID,
		Duration:    time.Since(start),
		Error:       err,
		ResultCount: len(results),
	})
	return results, err
}

// ---------------------------------------------------------------------------
// Explorer
// ---------------------------------------------------------------------------

func (t *tracedProvider) Describe(ctx context.Context, summaryID string) (*DescribeResult, error) {
	e, ok := t.inner.(Explorer)
	if !ok {
		return nil, errCapabilityNotSupported("Explorer")
	}
	start := time.Now()
	result, err := e.Describe(ctx, summaryID)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpDescribe,
		Duration: time.Since(start),
		Error:    err,
	})
	return result, err
}

func (t *tracedProvider) Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error) {
	e, ok := t.inner.(Explorer)
	if !ok {
		return nil, errCapabilityNotSupported("Explorer")
	}
	start := time.Now()
	result, err := e.Expand(ctx, summaryID, tokenCap)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpExpand,
		Duration: time.Since(start),
		Error:    err,
	})
	return result, err
}

// ---------------------------------------------------------------------------
// ProfileStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("ProfileStore")
	}
	start := time.Now()
	content, err := ps.GetProfile(ctx, userID, agentID)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpGetProfile,
		Duration: time.Since(start),
		Error:    err,
	})
	return content, err
}

func (t *tracedProvider) SetProfile(ctx context.Context, userID int64, agentID string, content string) error {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return errCapabilityNotSupported("ProfileStore")
	}
	start := time.Now()
	err := ps.SetProfile(ctx, userID, agentID, content)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpSetProfile,
		Duration: time.Since(start),
		Error:    err,
	})
	return err
}

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

func (t *tracedProvider) SaveInfo(ctx context.Context, info SessionInfo) error {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return errCapabilityNotSupported("SessionManager")
	}
	return sm.SaveInfo(ctx, info)
}

func (t *tracedProvider) LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return SessionInfo{}, errCapabilityNotSupported("SessionManager")
	}
	return sm.LoadInfo(ctx, sessionID)
}

func (t *tracedProvider) ListInfo(ctx context.Context, opts ListOptions) ([]SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	return sm.ListInfo(ctx, opts)
}

func (t *tracedProvider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	return sm.LoadHistory(ctx, sessionID)
}

// ---------------------------------------------------------------------------
// ReviewSource
// ---------------------------------------------------------------------------

func (t *tracedProvider) BuildReviewContext(ctx context.Context, session Session, since time.Time) (string, error) {
	rs, ok := t.inner.(ReviewSource)
	if !ok {
		return "", errCapabilityNotSupported("ReviewSource")
	}
	return rs.BuildReviewContext(ctx, session, since)
}

func (t *tracedProvider) MarkReviewed(ctx context.Context, session Session, at time.Time) error {
	rs, ok := t.inner.(ReviewSource)
	if !ok {
		return errCapabilityNotSupported("ReviewSource")
	}
	return rs.MarkReviewed(ctx, session, at)
}

func (t *tracedProvider) ListUnreviewed(ctx context.Context, agentID string, limit int) ([]ReviewCandidate, error) {
	rs, ok := t.inner.(ReviewSource)
	if !ok {
		return nil, errCapabilityNotSupported("ReviewSource")
	}
	return rs.ListUnreviewed(ctx, agentID, limit)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func errCapabilityNotSupported(name string) error {
	return &capabilityError{name: name}
}

type capabilityError struct {
	name string
}

func (e *capabilityError) Error() string {
	return "memory provider does not support " + e.name
}
