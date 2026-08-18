package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
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
// The returned Provider implements optional capability interfaces. Unsupported
// capabilities return sensible zero values or errors. Use [Unwrap] to check the
// inner provider directly.
//
// The Detail field is always populated with content previews. The trace hook
// decides whether to emit it based on log level (see [LevelTrace]).
func WithTracing(provider Provider, hooksFn func() *hooks.HookSet) Provider {
	return &tracedProvider{inner: provider, hooksFn: hooksFn}
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

func (t *tracedProvider) begin(ctx context.Context, hctx *hooks.PostMemoryCallContext) (context.Context, time.Time) {
	if authz.GuestIDFromContext(ctx) != "" {
		return ctx, time.Now()
	}
	hs := t.hooks()
	if hs == nil || hs.Empty() {
		return ctx, time.Now()
	}
	preResult, _ := hs.RunPreMemoryCall(ctx, &hooks.PreMemoryCallContext{
		HookMeta:  hctx.HookMeta,
		Op:        hctx.Op,
		SessionID: hctx.SessionID,
	})
	if preResult.Context != nil {
		ctx = preResult.Context
	}
	return ctx, time.Now()
}

func (t *tracedProvider) emit(ctx context.Context, hctx *hooks.PostMemoryCallContext) {
	if authz.GuestIDFromContext(ctx) != "" {
		return
	}
	hs := t.hooks()
	if hs == nil || hs.Empty() {
		return
	}
	hs.RunPostMemoryCall(ctx, hctx)
}

func (t *tracedProvider) finish(ctx context.Context, start time.Time, hctx *hooks.PostMemoryCallContext) {
	hctx.Duration = time.Since(start)
	t.emit(ctx, hctx)
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
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpBootstrap, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := t.inner.Bootstrap(ctx, session)
	hctx.Error = err
	t.finish(ctx, start, hctx)
	return err
}

func (t *tracedProvider) Append(ctx context.Context, session Session, msgs ...ai.Message) error {
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAppend, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := t.inner.Append(ctx, session, msgs...)
	hctx.Error = err
	hctx.MessageCount = len(msgs)
	hctx.Detail = formatMessages("appended", msgs)
	t.finish(ctx, start, hctx)
	return err
}

// SyncGroupEventsBefore traces the durable copy of public group events into a
// per-Agent LCM while preserving the optional capability through this wrapper.
func (t *tracedProvider) SyncGroupEventsBefore(ctx context.Context, session Session, triggerSeq int64) error {
	ingestor, ok := t.inner.(GroupEventIngestor)
	if !ok {
		return errCapabilityNotSupported("GroupEventIngestor")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAppend, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := ingestor.SyncGroupEventsBefore(ctx, session, triggerSeq)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("synchronized public group events before seq=%d", triggerSeq)
	t.finish(ctx, start, hctx)
	return err
}

// AppendGroupTurn traces the atomic append of the current public trigger and
// its private assistant/tool continuation.
func (t *tracedProvider) AppendGroupTurn(
	ctx context.Context,
	session Session,
	groupMessageID string,
	trigger ai.Message,
	continuation ...ai.Message,
) error {
	ingestor, ok := t.inner.(GroupEventIngestor)
	if !ok {
		return errCapabilityNotSupported("GroupEventIngestor")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAppend, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := ingestor.AppendGroupTurn(ctx, session, groupMessageID, trigger, continuation...)
	hctx.Error = err
	hctx.MessageCount = len(continuation) + 1
	hctx.Detail = fmt.Sprintf("appended group turn origin=%s", groupMessageID)
	t.finish(ctx, start, hctx)
	return err
}

// AppendInboxInput preserves the atomic durable-inbox capability through the
// tracing layer. Callers must inspect Unwrap first: this method exists on every
// traced provider so a wrapper-only type assertion is not a capability check.
func (t *tracedProvider) AppendInboxInput(ctx context.Context, session Session, inboxID string, msg ai.Message) error {
	appender, ok := t.inner.(InboxAppender)
	if !ok {
		return errCapabilityNotSupported("InboxAppender")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAppend, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := appender.AppendInboxInput(ctx, session, inboxID, msg)
	hctx.Error = err
	hctx.MessageCount = 1
	hctx.Detail = formatMessages("appended inbox input", []ai.Message{msg})
	t.finish(ctx, start, hctx)
	return err
}

func (t *tracedProvider) Assemble(ctx context.Context, session Session, budget, freshTail int) ([]ai.Message, error) {
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAssemble, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	msgs, err := t.inner.Assemble(ctx, session, budget, freshTail)
	var tokens int
	for _, m := range msgs {
		tokens += EstimateTokens(MessageText(m))
	}
	hctx.Error = err
	hctx.MessageCount = len(msgs)
	hctx.TokenCount = tokens
	hctx.Detail = formatMessages(fmt.Sprintf("assembled (budget=%d, freshTail=%d)", budget, freshTail), msgs)
	t.finish(ctx, start, hctx)
	return msgs, err
}

func (t *tracedProvider) Stats(ctx context.Context, session Session) (SessionStats, error) {
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpStats, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	stats, err := t.inner.Stats(ctx, session)
	hctx.Error = err
	hctx.TokenCount = stats.TokenCount
	hctx.SummaryCount = stats.SummaryCount
	t.finish(ctx, start, hctx)
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
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpNeedsCompaction, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	needs := c.NeedsCompaction(ctx, session, threshold)
	hctx.Detail = fmt.Sprintf("threshold=%.2f result=%v", threshold, needs)
	t.finish(ctx, start, hctx)
	return needs
}

func (t *tracedProvider) Compact(ctx context.Context, session Session, mode CompactionMode) (*CompactionResult, error) {
	c, ok := t.inner.(Compactor)
	if !ok {
		return nil, errCapabilityNotSupported("Compactor")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpCompact, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	result, err := c.Compact(ctx, session, mode)
	hctx.Error = err
	if result != nil {
		hctx.SummaryCount = result.LeafSummariesCreated + result.CondensedSummariesCreated
		hctx.TokenCount = result.TokensAfter
		hctx.TokenDelta = result.TokensAfter - result.TokensBefore
		hctx.Detail = fmt.Sprintf("leaf=%d condensed=%d compacted=%d tokens=%d→%d (Δ%d) duration=%s",
			result.LeafSummariesCreated, result.CondensedSummariesCreated,
			result.MessagesCompacted, result.TokensBefore, result.TokensAfter,
			result.TokensAfter-result.TokensBefore, result.Duration.Round(time.Millisecond))
	}
	t.finish(ctx, start, hctx)
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
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpSearch, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	results, err := s.Search(ctx, session, query)
	hctx.Error = err
	hctx.ResultCount = len(results)
	hctx.Detail = formatSearchResults(query, results)
	t.finish(ctx, start, hctx)
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
	hctx := &hooks.PostMemoryCallContext{
		HookMeta:  hooks.HookMeta{UserID: authz.UserIDFromContext(ctx), AgentID: authz.AgentIDFromContext(ctx)},
		SessionID: SessionIDFromContext(ctx),
		Op:        hooks.MemoryOpDescribe,
	}
	ctx, start := t.begin(ctx, hctx)
	result, err := e.Describe(ctx, summaryID)
	hctx.Error = err
	if result != nil {
		hctx.Detail = fmt.Sprintf("summary=%s kind=%s depth=%d descendants=%d content=%s",
			result.SummaryID, result.Kind, result.Depth, result.DescendantCount,
			truncateStr(result.Content, 200))
	}
	t.finish(ctx, start, hctx)
	return result, err
}

func (t *tracedProvider) Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error) {
	e, ok := t.inner.(Explorer)
	if !ok {
		return nil, errCapabilityNotSupported("Explorer")
	}
	hctx := &hooks.PostMemoryCallContext{
		HookMeta:  hooks.HookMeta{UserID: authz.UserIDFromContext(ctx), AgentID: authz.AgentIDFromContext(ctx)},
		SessionID: SessionIDFromContext(ctx),
		Op:        hooks.MemoryOpExpand,
	}
	ctx, start := t.begin(ctx, hctx)
	result, err := e.Expand(ctx, summaryID, tokenCap)
	hctx.Error = err
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
	t.finish(ctx, start, hctx)
	return result, err
}

// ---------------------------------------------------------------------------
// MessageReader
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetMessage(ctx context.Context, messageID string) (*MessageDetail, error) {
	r, ok := t.inner.(MessageReader)
	if !ok {
		return nil, errCapabilityNotSupported("MessageReader")
	}
	hctx := &hooks.PostMemoryCallContext{
		HookMeta:  hooks.HookMeta{UserID: authz.UserIDFromContext(ctx), AgentID: authz.AgentIDFromContext(ctx)},
		SessionID: SessionIDFromContext(ctx),
		Op:        hooks.MemoryOpGetMessage,
	}
	ctx, start := t.begin(ctx, hctx)
	result, err := r.GetMessage(ctx, messageID)
	hctx.Error = err
	if result != nil {
		hctx.Detail = fmt.Sprintf("message=%s session=%s role=%s content=%s",
			result.MessageID, result.SessionID, result.Role, truncateStr(result.Content, 200))
	}
	t.finish(ctx, start, hctx)
	return result, err
}

// ---------------------------------------------------------------------------
// ProfileStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetProfile(ctx context.Context, userID string, agentID string) (string, error) {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("ProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpGetProfile}
	ctx, start := t.begin(ctx, hctx)
	content, err := ps.GetProfile(ctx, userID, agentID)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s len=%d content=%s", userID, agentID, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
	return content, err
}

func (t *tracedProvider) SetProfile(ctx context.Context, userID string, agentID string, content string) error {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return errCapabilityNotSupported("ProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpSetProfile}
	ctx, start := t.begin(ctx, hctx)
	err := ps.SetProfile(ctx, userID, agentID, content)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s len=%d content=%s", userID, agentID, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
	return err
}

func (t *tracedProvider) GetAgentSoul(ctx context.Context, userID string, agentID string) (string, error) {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("ProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpGetAgentSoul}
	ctx, start := t.begin(ctx, hctx)
	content, err := ps.GetAgentSoul(ctx, userID, agentID)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s len=%d content=%s", userID, agentID, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
	return content, err
}

func (t *tracedProvider) SetAgentSoul(ctx context.Context, userID string, agentID string, content string) error {
	ps, ok := t.inner.(ProfileStore)
	if !ok {
		return errCapabilityNotSupported("ProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpSetAgentSoul}
	ctx, start := t.begin(ctx, hctx)
	err := ps.SetAgentSoul(ctx, userID, agentID, content)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s len=%d content=%s", userID, agentID, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
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

func (t *tracedProvider) ReadChangelog(ctx context.Context, userID string, agentID string, scope string, limit int) ([]ChangeEntry, error) {
	cr, ok := t.inner.(ChangelogReader)
	if !ok {
		return nil, errCapabilityNotSupported("ChangelogReader")
	}
	return cr.ReadChangelog(ctx, userID, agentID, scope, limit)
}

// ReadChangelogPage forwards the optional keyset reader through tracing.
func (t *tracedProvider) ReadChangelogPage(ctx context.Context, userID string, agentID string, scope string, cursor *ChangelogCursor, limit int) ([]ChangeEntry, error) {
	cr, ok := t.inner.(ChangelogPageReader)
	if !ok {
		return nil, errCapabilityNotSupported("ChangelogPageReader")
	}
	return cr.ReadChangelogPage(ctx, userID, agentID, scope, cursor, limit)
}

// ---------------------------------------------------------------------------
// ConstraintStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetConstraints(ctx context.Context, userID string, agentID string) ([]ConstraintEntry, error) {
	cs, ok := t.inner.(ConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("ConstraintStore")
	}
	return cs.GetConstraints(ctx, userID, agentID)
}

func (t *tracedProvider) AddConstraint(ctx context.Context, userID string, agentID string, text string) ([]ConstraintEntry, error) {
	cs, ok := t.inner.(ConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("ConstraintStore")
	}
	return cs.AddConstraint(ctx, userID, agentID, text)
}

func (t *tracedProvider) RemoveConstraint(ctx context.Context, userID string, agentID string, id string) ([]ConstraintEntry, error) {
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
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: info.ID, UserID: info.UserID, AgentID: info.AgentID}, Op: hooks.MemoryOpSaveInfo, SessionID: info.ID}
	ctx, start := t.begin(ctx, hctx)
	err := sm.SaveInfo(ctx, info)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("title=%q archived=%v channel=%s", info.Title, info.Archived, info.Channel)
	t.finish(ctx, start, hctx)
	return err
}

func (t *tracedProvider) ArchiveInfo(ctx context.Context, info SessionInfo) (bool, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return false, errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: info.ID, UserID: info.UserID, AgentID: info.AgentID}, Op: hooks.MemoryOpArchiveInfo, SessionID: info.ID}
	ctx, start := t.begin(ctx, hctx)
	applied, err := sm.ArchiveInfo(ctx, info)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("applied=%v", applied)
	t.finish(ctx, start, hctx)
	return applied, err
}

func (t *tracedProvider) TouchActiveInfo(ctx context.Context, info SessionInfo) (bool, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return false, errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: info.ID, UserID: info.UserID, AgentID: info.AgentID}, Op: hooks.MemoryOpTouchActiveInfo, SessionID: info.ID}
	ctx, start := t.begin(ctx, hctx)
	applied, err := sm.TouchActiveInfo(ctx, info)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("applied=%v title=%q channel=%s", applied, info.Title, info.Channel)
	t.finish(ctx, start, hctx)
	return applied, err
}

func (t *tracedProvider) RotateInfo(ctx context.Context, expectedSessionID string, successor SessionInfo) error {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: successor.ID, UserID: successor.UserID, AgentID: successor.AgentID}, Op: hooks.MemoryOpRotateInfo, SessionID: successor.ID}
	ctx, start := t.begin(ctx, hctx)
	err := sm.RotateInfo(ctx, expectedSessionID, successor)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("expected=%s successor=%s kind=%s", expectedSessionID, successor.ID, successor.Kind)
	t.finish(ctx, start, hctx)
	return err
}

// CommitGroupCursor traces cursor movement and fails closed when the wrapped
// provider cannot make the corresponding durable update.
func (t *tracedProvider) CommitGroupCursor(ctx context.Context, session Session, triggerSeq int64) error {
	committer, ok := t.inner.(GroupCursorCommitter)
	if !ok {
		return errCapabilityNotSupported("GroupCursorCommitter")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpAppend, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	err := committer.CommitGroupCursor(ctx, session, triggerSeq)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("committed public group cursor through seq=%d", triggerSeq)
	t.finish(ctx, start, hctx)
	return err
}

// Session activity is durable session metadata rather than memory content, so
// the tracing wrapper preserves the optional capability without emitting a
// memory hook for each turn-state write.
func (t *tracedProvider) MarkSessionTurnStarted(ctx context.Context, session Session) (bool, error) {
	activity, ok := t.inner.(SessionActivityStore)
	if !ok {
		return false, errCapabilityNotSupported("SessionActivityStore")
	}
	return activity.MarkSessionTurnStarted(ctx, session)
}

func (t *tracedProvider) MarkSessionTurnCompleted(ctx context.Context, session Session, result SessionTurnResult) (bool, error) {
	activity, ok := t.inner.(SessionActivityStore)
	if !ok {
		return false, errCapabilityNotSupported("SessionActivityStore")
	}
	return activity.MarkSessionTurnCompleted(ctx, session, result)
}

func (t *tracedProvider) MarkSessionViewed(ctx context.Context, session Session) (bool, error) {
	activity, ok := t.inner.(SessionActivityStore)
	if !ok {
		return false, errCapabilityNotSupported("SessionActivityStore")
	}
	return activity.MarkSessionViewed(ctx, session)
}

func (t *tracedProvider) LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return SessionInfo{}, errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: sessionID}, Op: hooks.MemoryOpLoadInfo, SessionID: sessionID}
	ctx, start := t.begin(ctx, hctx)
	info, err := sm.LoadInfo(ctx, sessionID)
	hctx.UserID = info.UserID
	hctx.AgentID = info.AgentID
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("title=%q archived=%v channel=%s", info.Title, info.Archived, info.Channel)
	t.finish(ctx, start, hctx)
	return info, err
}

func (t *tracedProvider) ListInfo(ctx context.Context, opts ListOptions) ([]SessionInfo, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: opts.UserID, AgentID: opts.AgentID}, Op: hooks.MemoryOpListInfo}
	ctx, start := t.begin(ctx, hctx)
	infos, err := sm.ListInfo(ctx, opts)
	hctx.Error = err
	hctx.ResultCount = len(infos)
	hctx.Detail = fmt.Sprintf("agent=%s user=%s archived=%v limit=%d → %d results", opts.AgentID, opts.UserID, opts.IncludeArchived, opts.Limit, len(infos))
	t.finish(ctx, start, hctx)
	return infos, err
}

// ListInfoForReview forwards the optional review-listing capability so that
// callers (e.g. reflect) can list candidates across users without a user scope.
// Without this passthrough the wrapper hides the inner method and callers fall
// back to ListInfo, which requires a user context and fails.
func (t *tracedProvider) ListInfoForReview(ctx context.Context, opts ListOptions) ([]SessionInfo, error) {
	lister, ok := t.inner.(interface {
		ListInfoForReview(ctx context.Context, opts ListOptions) ([]SessionInfo, error)
	})
	if !ok {
		return nil, errCapabilityNotSupported("ListInfoForReview")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{AgentID: opts.AgentID}, Op: hooks.MemoryOpListInfoForReview}
	ctx, start := t.begin(ctx, hctx)
	infos, err := lister.ListInfoForReview(ctx, opts)
	hctx.Error = err
	hctx.ResultCount = len(infos)
	hctx.Detail = fmt.Sprintf("agent=%s limit=%d → %d results", opts.AgentID, opts.Limit, len(infos))
	t.finish(ctx, start, hctx)
	return infos, err
}

// ListInfoForAdmin preserves the optional administrative listing capability.
// It deliberately bypasses memory hooks: listing guest metadata must not enter
// any user or guest memory lifecycle.
func (t *tracedProvider) ListInfoForAdmin(ctx context.Context, opts ListOptions) ([]SessionInfo, error) {
	lister, ok := t.inner.(interface {
		ListInfoForAdmin(ctx context.Context, opts ListOptions) ([]SessionInfo, error)
	})
	if !ok {
		return nil, errCapabilityNotSupported("ListInfoForAdmin")
	}
	return lister.ListInfoForAdmin(ctx, opts)
}

func (t *tracedProvider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	sm, ok := t.inner.(SessionManager)
	if !ok {
		return nil, errCapabilityNotSupported("SessionManager")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: sessionID}, Op: hooks.MemoryOpLoadHistory, SessionID: sessionID}
	ctx, start := t.begin(ctx, hctx)
	msgs, err := sm.LoadHistory(ctx, sessionID)
	hctx.Error = err
	hctx.MessageCount = len(msgs)
	hctx.Detail = formatMessages("loaded history", msgs)
	t.finish(ctx, start, hctx)
	return msgs, err
}

// LoadReviewHistory forwards stable storage boundaries used by background
// reviewers. Keeping this capability visible through the tracing wrapper avoids
// falling back to timestamp-only LoadHistory processing.
func (t *tracedProvider) LoadReviewHistory(ctx context.Context, sessionID string) ([]ReviewMessage, error) {
	rr, ok := t.inner.(ReviewHistoryReader)
	if !ok {
		return nil, errCapabilityNotSupported("ReviewHistoryReader")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: sessionID}, Op: hooks.MemoryOpLoadReviewHistory, SessionID: sessionID}
	ctx, start := t.begin(ctx, hctx)
	msgs, err := rr.LoadReviewHistory(ctx, sessionID)
	hctx.Error = err
	hctx.MessageCount = len(msgs)
	raw := make([]ai.Message, 0, len(msgs))
	for _, msg := range msgs {
		raw = append(raw, msg.Message)
	}
	hctx.Detail = formatMessages("loaded review history", raw)
	t.finish(ctx, start, hctx)
	return msgs, err
}

// ---------------------------------------------------------------------------
// VersionedProfileStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetProfileAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	vp, ok := t.inner.(VersionedProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("VersionedProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpGetProfileAt}
	ctx, start := t.begin(ctx, hctx)
	content, err := vp.GetProfileAt(ctx, userID, agentID, version)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s version=%d len=%d content=%s", userID, agentID, version, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
	return content, err
}

func (t *tracedProvider) GetAgentSoulAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	vp, ok := t.inner.(VersionedProfileStore)
	if !ok {
		return "", errCapabilityNotSupported("VersionedProfileStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpGetAgentSoulAt}
	ctx, start := t.begin(ctx, hctx)
	content, err := vp.GetAgentSoulAt(ctx, userID, agentID, version)
	hctx.Error = err
	hctx.Detail = fmt.Sprintf("user=%s agent=%s version=%d len=%d content=%s", userID, agentID, version, len(content), truncateStr(content, 300))
	t.finish(ctx, start, hctx)
	return content, err
}

// ---------------------------------------------------------------------------
// VersionedConstraintStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetConstraintsAt(ctx context.Context, userID string, agentID string, version int64) ([]ConstraintEntry, error) {
	vc, ok := t.inner.(VersionedConstraintStore)
	if !ok {
		return nil, errCapabilityNotSupported("VersionedConstraintStore")
	}
	return vc.GetConstraintsAt(ctx, userID, agentID, version)
}

// ---------------------------------------------------------------------------
// SessionSnapshotStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetOrCreateSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) (SessionSnapshot, error) {
	sss, ok := t.inner.(SessionSnapshotStore)
	if !ok {
		return SessionSnapshot{}, errCapabilityNotSupported("SessionSnapshotStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: sessionID, UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpGetOrCreateSessionSnapshot, SessionID: sessionID}
	ctx, start := t.begin(ctx, hctx)
	snap, err := sss.GetOrCreateSessionSnapshot(ctx, sessionID, userID, agentID)
	hctx.Error = err
	if err == nil {
		hctx.Detail = fmt.Sprintf("version=%d updated_at=%s", snap.Version, snap.UpdatedAt.Format(time.RFC3339))
	}
	t.finish(ctx, start, hctx)
	return snap, err
}

func (t *tracedProvider) AdvanceSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) error {
	sss, ok := t.inner.(SessionSnapshotStore)
	if !ok {
		return errCapabilityNotSupported("SessionSnapshotStore")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{SessionID: sessionID, UserID: userID, AgentID: agentID}, Op: hooks.MemoryOpAdvanceSessionSnapshot, SessionID: sessionID}
	ctx, start := t.begin(ctx, hctx)
	err := sss.AdvanceSessionSnapshot(ctx, sessionID, userID, agentID)
	hctx.Error = err
	t.finish(ctx, start, hctx)
	return err
}

// ---------------------------------------------------------------------------
// FactStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) ListActiveFacts(ctx context.Context, userID string, agentID string, subject FactSubject) ([]Fact, error) {
	fs, ok := t.inner.(FactStore)
	if !ok {
		return nil, errCapabilityNotSupported("FactStore")
	}
	return fs.ListActiveFacts(ctx, userID, agentID, subject)
}

func (t *tracedProvider) ListActiveFactsAt(ctx context.Context, userID string, agentID string, subject FactSubject, version int64) ([]Fact, error) {
	fs, ok := t.inner.(VersionedFactStore)
	if !ok {
		return nil, errCapabilityNotSupported("VersionedFactStore")
	}
	return fs.ListActiveFactsAt(ctx, userID, agentID, subject, version)
}

func (t *tracedProvider) TouchKnowledgeUsage(ctx context.Context, userID string, agentID string, factIDs []string) error {
	kt, ok := t.inner.(KnowledgeUsageTracker)
	if !ok {
		return errCapabilityNotSupported("KnowledgeUsageTracker")
	}
	return kt.TouchKnowledgeUsage(ctx, userID, agentID, factIDs)
}

// ---------------------------------------------------------------------------
// ProfileEntryStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetProfileEntries(ctx context.Context, userID string, agentID string) ([]ProfileEntry, error) {
	pes, ok := t.inner.(ProfileEntryStore)
	if !ok {
		return nil, errCapabilityNotSupported("ProfileEntryStore")
	}
	entries, err := pes.GetProfileEntries(ctx, userID, agentID)
	return entries, err
}

// ---------------------------------------------------------------------------
// GroupMemoryStore
// ---------------------------------------------------------------------------

func (t *tracedProvider) GetGroupMemory(ctx context.Context, groupID string) (string, error) {
	gms, ok := t.inner.(GroupMemoryStore)
	if !ok {
		return "", errCapabilityNotSupported("GroupMemoryStore")
	}
	content, err := gms.GetGroupMemory(ctx, groupID)
	return content, err
}

// ---------------------------------------------------------------------------
// Reviewer
// ---------------------------------------------------------------------------

func (t *tracedProvider) BuildReviewContext(ctx context.Context, session Session, since time.Time) (string, error) {
	rv, ok := t.inner.(Reviewer)
	if !ok {
		return "", errCapabilityNotSupported("Reviewer")
	}
	hctx := &hooks.PostMemoryCallContext{HookMeta: metaFromSession(session), Op: hooks.MemoryOpBuildReview, SessionID: session.ID}
	ctx, start := t.begin(ctx, hctx)
	text, err := rv.BuildReviewContext(ctx, session, since)
	hctx.Error = err
	hctx.TokenCount = EstimateTokens(text)
	hctx.Detail = fmt.Sprintf("since=%s len=%d", since.Format(time.RFC3339), len(text))
	t.finish(ctx, start, hctx)
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
