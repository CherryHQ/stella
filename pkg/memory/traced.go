package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
// Searcher, Explorer, ProfileStore, SessionManager, Reviewer). Methods for
// capabilities not supported by the inner provider return sensible zero values or errors.
// Use [Unwrap] to check the inner provider's actual capabilities.
//
// The Detail field is always populated with content previews. The trace hook
// decides whether to emit it based on log level (see [LevelTrace]).
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

// metaFromSession populates HookMeta from a memory Session.
func metaFromSession(s Session) hooks.HookMeta {
	return hooks.HookMeta{
		SessionID: s.ID,
		UserID:    s.UserID,
		AgentID:   s.AgentID,
	}
}

// ---------------------------------------------------------------------------
// Provider (core interface)
// ---------------------------------------------------------------------------

func (t *tracedProvider) Name() string { return t.inner.Name() }

func (t *tracedProvider) Bootstrap(ctx context.Context, session Session) error {
	start := time.Now()
	err := t.inner.Bootstrap(ctx, session)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:  metaFromSession(session),
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
		HookMeta:     metaFromSession(session),
		Op:           hooks.MemoryOpAppend,
		SessionID:    session.ID,
		Duration:     time.Since(start),
		Error:        err,
		MessageCount: len(msgs),
		Detail:       formatMessages("appended", msgs),
	})
	return err
}

func (t *tracedProvider) Assemble(ctx context.Context, session Session, budget, freshTail int) ([]ai.Message, error) {
	start := time.Now()
	msgs, err := t.inner.Assemble(ctx, session, budget, freshTail)
	var tokens int
	for _, m := range msgs {
		tokens += EstimateTokens(MessageText(m))
	}
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:     metaFromSession(session),
		Op:           hooks.MemoryOpAssemble,
		SessionID:    session.ID,
		Duration:     time.Since(start),
		Error:        err,
		MessageCount: len(msgs),
		TokenCount:   tokens,
		Detail:       formatMessages(fmt.Sprintf("assembled (budget=%d, freshTail=%d)", budget, freshTail), msgs),
	})
	return msgs, err
}

func (t *tracedProvider) Stats(ctx context.Context, session Session) (SessionStats, error) {
	start := time.Now()
	stats, err := t.inner.Stats(ctx, session)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:     metaFromSession(session),
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
	start := time.Now()
	needs := c.NeedsCompaction(ctx, session, threshold)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:  metaFromSession(session),
		Op:        hooks.MemoryOpNeedsCompaction,
		SessionID: session.ID,
		Duration:  time.Since(start),
		Detail:    fmt.Sprintf("threshold=%.2f result=%v", threshold, needs),
	})
	return needs
}

func (t *tracedProvider) Compact(ctx context.Context, session Session, mode CompactionMode) (*CompactionResult, error) {
	c, ok := t.inner.(Compactor)
	if !ok {
		return nil, errCapabilityNotSupported("Compactor")
	}
	start := time.Now()
	result, err := c.Compact(ctx, session, mode)
	hctx := &hooks.PostMemoryCallContext{
		HookMeta:  metaFromSession(session),
		Op:        hooks.MemoryOpCompact,
		SessionID: session.ID,
		Duration:  time.Since(start),
		Error:     err,
	}
	if result != nil {
		hctx.SummaryCount = result.LeafSummariesCreated + result.CondensedSummariesCreated
		hctx.TokenCount = result.TokensAfter
		hctx.TokenDelta = result.TokensAfter - result.TokensBefore
		hctx.Detail = fmt.Sprintf("leaf=%d condensed=%d compacted=%d tokens=%d→%d (Δ%d) duration=%s",
			result.LeafSummariesCreated, result.CondensedSummariesCreated,
			result.MessagesCompacted, result.TokensBefore, result.TokensAfter,
			result.TokensAfter-result.TokensBefore, result.Duration.Round(time.Millisecond))
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
		HookMeta:    metaFromSession(session),
		Op:          hooks.MemoryOpSearch,
		SessionID:   session.ID,
		Duration:    time.Since(start),
		Error:       err,
		ResultCount: len(results),
		Detail:      formatSearchResults(query, results),
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
	hctx := &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpDescribe,
		Duration: time.Since(start),
		Error:    err,
	}
	if result != nil {
		hctx.Detail = fmt.Sprintf("summary=%s kind=%s depth=%d descendants=%d content=%s",
			result.SummaryID, result.Kind, result.Depth, result.DescendantCount,
			truncateStr(result.Content, 200))
	}
	t.emit(ctx, hctx)
	return result, err
}

func (t *tracedProvider) Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error) {
	e, ok := t.inner.(Explorer)
	if !ok {
		return nil, errCapabilityNotSupported("Explorer")
	}
	start := time.Now()
	result, err := e.Expand(ctx, summaryID, tokenCap)
	hctx := &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpExpand,
		Duration: time.Since(start),
		Error:    err,
	}
	if result != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "summary=%s", result.SummaryID)
		if len(result.Messages) > 0 {
			fmt.Fprintf(&b, " messages=%d", len(result.Messages))
			for i, m := range result.Messages {
				if i >= 5 {
					fmt.Fprintf(&b, "\n  ... +%d more", len(result.Messages)-5)
					break
				}
				fmt.Fprintf(&b, "\n  [%s] %s", m.Role, truncateStr(m.Content, 100))
			}
		}
		if len(result.Children) > 0 {
			fmt.Fprintf(&b, " children=%d", len(result.Children))
			for i, c := range result.Children {
				if i >= 5 {
					fmt.Fprintf(&b, "\n  ... +%d more", len(result.Children)-5)
					break
				}
				fmt.Fprintf(&b, "\n  [%s d%d] %s", c.Kind, c.Depth, truncateStr(c.Content, 100))
			}
		}
		hctx.Detail = b.String()
	}
	t.emit(ctx, hctx)
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
		HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID},
		Op:       hooks.MemoryOpGetProfile,
		Duration: time.Since(start),
		Error:    err,
		Detail: fmt.Sprintf("user=%d agent=%s len=%d content=%s",
			userID, agentID, len(content), truncateStr(content, 300)),
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
		HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID},
		Op:       hooks.MemoryOpSetProfile,
		Duration: time.Since(start),
		Error:    err,
		Detail: fmt.Sprintf("user=%d agent=%s len=%d content=%s",
			userID, agentID, len(content), truncateStr(content, 300)),
	})
	return err
}

func (t *tracedProvider) GetAgentSoul(ctx context.Context, userID int64, agentID string) (string, error) {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("ProfileStore")
	}
	start := time.Now()
	content, err := ps.GetAgentSoul(ctx, userID, agentID)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID},
		Op:       hooks.MemoryOpGetAgentSoul,
		Duration: time.Since(start),
		Error:    err,
		Detail: fmt.Sprintf("user=%d agent=%s len=%d content=%s",
			userID, agentID, len(content), truncateStr(content, 300)),
	})
	return content, err
}

func (t *tracedProvider) SetAgentSoul(ctx context.Context, userID int64, agentID string, content string) error {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return errCapabilityNotSupported("ProfileStore")
	}
	start := time.Now()
	err := ps.SetAgentSoul(ctx, userID, agentID, content)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID},
		Op:       hooks.MemoryOpSetAgentSoul,
		Duration: time.Since(start),
		Error:    err,
		Detail: fmt.Sprintf("user=%d agent=%s len=%d content=%s",
			userID, agentID, len(content), truncateStr(content, 300)),
	})
	return err
}

// ---------------------------------------------------------------------------
// ChangelogWriter / ChangelogReader
// ---------------------------------------------------------------------------

func (t *tracedProvider) WriteChangelog(ctx context.Context, entry ChangeEntry) error {
	cw, ok := t.inner.(ChangelogWriter)
	if !ok {
		return errCapabilityNotSupported("ChangelogWriter")
	}
	return cw.WriteChangelog(ctx, entry)
}

func (t *tracedProvider) ReadChangelog(ctx context.Context, userID int64, agentID string, scope string, limit int) ([]ChangeEntry, error) {
	cr, ok := t.inner.(ChangelogReader)
	if !ok {
		return nil, errCapabilityNotSupported("ChangelogReader")
	}
	return cr.ReadChangelog(ctx, userID, agentID, scope, limit)
}

// ---------------------------------------------------------------------------
// ConstraintStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetConstraints(ctx context.Context, userID int64, agentID string) ([]ConstraintEntry, error) {
	cs, ok := t.inner.(ConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("ConstraintStore")
	}
	return cs.GetConstraints(ctx, userID, agentID)
}

func (t *tracedProvider) AddConstraint(ctx context.Context, userID int64, agentID string, text string) ([]ConstraintEntry, error) {
	cs, ok := t.inner.(ConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("ConstraintStore")
	}
	return cs.AddConstraint(ctx, userID, agentID, text)
}

func (t *tracedProvider) RemoveConstraint(ctx context.Context, userID int64, agentID string, id string) ([]ConstraintEntry, error) {
	cs, ok := t.inner.(ConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("ConstraintStore")
	}
	return cs.RemoveConstraint(ctx, userID, agentID, id)
}

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

func (t *tracedProvider) SaveInfo(ctx context.Context, info SessionInfo) error {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return errCapabilityNotSupported("SessionManager")
	}
	start := time.Now()
	err := sm.SaveInfo(ctx, info)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:  hooks.HookMeta{SessionID: info.ID, UserID: info.UserID, AgentID: info.AgentID},
		Op:        hooks.MemoryOpSaveInfo,
		SessionID: info.ID,
		Duration:  time.Since(start),
		Error:     err,
		Detail:    fmt.Sprintf("title=%q archived=%v channel=%s", info.Title, info.Archived, info.Channel),
	})
	return err
}

func (t *tracedProvider) LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return SessionInfo{}, errCapabilityNotSupported("SessionManager")
	}
	start := time.Now()
	info, err := sm.LoadInfo(ctx, sessionID)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:  hooks.HookMeta{SessionID: sessionID, UserID: info.UserID, AgentID: info.AgentID},
		Op:        hooks.MemoryOpLoadInfo,
		SessionID: sessionID,
		Duration:  time.Since(start),
		Error:     err,
		Detail:    fmt.Sprintf("title=%q archived=%v channel=%s", info.Title, info.Archived, info.Channel),
	})
	return info, err
}

func (t *tracedProvider) ListInfo(ctx context.Context, opts ListOptions) ([]SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	start := time.Now()
	infos, err := sm.ListInfo(ctx, opts)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:          hooks.MemoryOpListInfo,
		Duration:    time.Since(start),
		Error:       err,
		ResultCount: len(infos),
		Detail:      fmt.Sprintf("agent=%s user=%d archived=%v limit=%d → %d results", opts.AgentID, opts.UserID, opts.IncludeArchived, opts.Limit, len(infos)),
	})
	return infos, err
}

func (t *tracedProvider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	start := time.Now()
	msgs, err := sm.LoadHistory(ctx, sessionID)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		Op:           hooks.MemoryOpLoadHistory,
		SessionID:    sessionID,
		Duration:     time.Since(start),
		Error:        err,
		MessageCount: len(msgs),
		Detail:       formatMessages("loaded history", msgs),
	})
	return msgs, err
}

// ---------------------------------------------------------------------------
// VersionedProfileStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetProfileAt(ctx context.Context, userID int64, agentID string, version int64) (string, error) {
	vp, ok := t.inner.(VersionedProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("VersionedProfileStore")
	}
	return vp.GetProfileAt(ctx, userID, agentID, version)
}

func (t *tracedProvider) GetAgentSoulAt(ctx context.Context, userID int64, agentID string, version int64) (string, error) {
	vp, ok := t.inner.(VersionedProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("VersionedProfileStore")
	}
	return vp.GetAgentSoulAt(ctx, userID, agentID, version)
}

// ---------------------------------------------------------------------------
// VersionedConstraintStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetConstraintsAt(ctx context.Context, userID int64, agentID string, version int64) ([]ConstraintEntry, error) {
	vc, ok := t.inner.(VersionedConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("VersionedConstraintStore")
	}
	return vc.GetConstraintsAt(ctx, userID, agentID, version)
}

// ---------------------------------------------------------------------------
// SessionSnapshotStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetOrCreateSessionSnapshot(ctx context.Context, sessionID string, userID int64, agentID string) (SessionSnapshot, error) {
	sss, ok := t.inner.(SessionSnapshotStore)
	if !ok {
		return SessionSnapshot{}, errCapabilityNotSupported("SessionSnapshotStore")
	}
	return sss.GetOrCreateSessionSnapshot(ctx, sessionID, userID, agentID)
}

func (t *tracedProvider) AdvanceSessionSnapshot(ctx context.Context, sessionID string, userID int64, agentID string) error {
	sss, ok := t.inner.(SessionSnapshotStore)
	if !ok {
		return errCapabilityNotSupported("SessionSnapshotStore")
	}
	return sss.AdvanceSessionSnapshot(ctx, sessionID, userID, agentID)
}

// ---------------------------------------------------------------------------
// Reviewer
// ---------------------------------------------------------------------------

func (t *tracedProvider) BuildReviewContext(ctx context.Context, session Session, since time.Time) (string, error) {
	rv, ok := t.inner.(Reviewer)
	if !ok {
		return "", errCapabilityNotSupported("Reviewer")
	}
	start := time.Now()
	text, err := rv.BuildReviewContext(ctx, session, since)
	t.emit(ctx, &hooks.PostMemoryCallContext{
		HookMeta:   metaFromSession(session),
		Op:         hooks.MemoryOpBuildReview,
		SessionID:  session.ID,
		Duration:   time.Since(start),
		Error:      err,
		TokenCount: EstimateTokens(text),
		Detail:     fmt.Sprintf("since=%s len=%d", since.Format(time.RFC3339), len(text)),
	})
	return text, err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// formatMessages builds a human-readable preview of messages for the Detail field.
func formatMessages(label string, msgs []ai.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d messages:", label, len(msgs))
	for i, m := range msgs {
		if i >= 10 {
			fmt.Fprintf(&b, "\n  ... +%d more", len(msgs)-10)
			break
		}
		role := MessageRole(m)
		text := truncateStr(MessageText(m), 150)
		tokens := EstimateTokens(MessageText(m))
		fmt.Fprintf(&b, "\n  [%s] (~%d tok) %s", role, tokens, text)
	}
	return b.String()
}

// formatSearchResults builds a detail string for search operations.
func formatSearchResults(query SearchQuery, results []SearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "query=%q scope=%d limit=%d → %d results", query.Text, query.Scope, query.Limit, len(results))
	for i, r := range results {
		if i >= 5 {
			fmt.Fprintf(&b, "\n  ... +%d more", len(results)-5)
			break
		}
		fmt.Fprintf(&b, "\n  [%s %s] %s", r.SourceType, r.SourceID, truncateStr(r.Content, 100))
	}
	return b.String()
}

// truncateStr truncates a string at the given rune count, appending "..." if truncated.
func truncateStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}

func errCapabilityNotSupported(name string) error {
	return &capabilityError{name: name}
}

type capabilityError struct {
	name string
}

func (e *capabilityError) Error() string {
	return "memory provider does not support " + e.name
}
