// Package memorytest provides test doubles and conformance testing for
// [memory.Provider] implementations.
package memorytest

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// FakeSummary holds summary data for the Explorer capability.
type FakeSummary struct {
	ID              string
	Kind            string // "leaf" or "condensed"
	Depth           int
	Content         string
	EarliestAt      *time.Time
	LatestAt        *time.Time
	DescendantCount int
	ParentIDs       []string
	ChildIDs        []string
	// For leaf: source messages. For condensed: child summaries.
	SourceMessages []memory.ExpandMessage
	ChildSummaries []memory.ExpandChild
}

// fakeSessionInfo holds session metadata.
type fakeSessionInfo struct {
	info memory.SessionInfo
}

// Fake is an in-memory Provider that implements all optional capability interfaces.
// Use it in unit tests to avoid database setup.
type Fake struct {
	// sessions maps session ID -> ordered slice of ai.Message.
	sessions map[string][]ai.Message
	// profiles maps "userID:agentID" -> content.
	profiles map[string]string
	// souls maps "userID:agentID" -> content.
	souls map[string]string
	// constraints maps "userID:agentID" -> []ConstraintEntry.
	constraints map[string][]memory.ConstraintEntry
	// profileEntries maps "userID:agentID" -> []ProfileEntry.
	profileEntries map[string][]memory.ProfileEntry
	// facts maps "userID:agentID" -> []Fact.
	facts map[string][]memory.Fact
	// summaries maps summary ID -> FakeSummary.
	summaries map[string]FakeSummary
	// sessionInfos maps session ID -> fakeSessionInfo.
	sessionInfos map[string]fakeSessionInfo
	// bootstrapped tracks which sessions have been bootstrapped.
	bootstrapped map[string]bool
	// changelog holds all recorded change entries in insertion order.
	changelog []memory.ChangeEntry
	// snapshots maps "sessionID:userID:agentID" -> SessionSnapshot.
	snapshots map[string]memory.SessionSnapshot
	// rotateErr, when set, fails every RotateInfo (see FailRotation).
	rotateErr error
	// mu protects all maps.
	mu sync.Mutex
}

// New creates a new Fake provider ready for use.
func New() *Fake {
	return &Fake{
		sessions:       make(map[string][]ai.Message),
		profiles:       make(map[string]string),
		souls:          make(map[string]string),
		constraints:    make(map[string][]memory.ConstraintEntry),
		profileEntries: make(map[string][]memory.ProfileEntry),
		facts:          make(map[string][]memory.Fact),
		summaries:      make(map[string]FakeSummary),
		sessionInfos:   make(map[string]fakeSessionInfo),
		bootstrapped:   make(map[string]bool),
		snapshots:      make(map[string]memory.SessionSnapshot),
	}
}

// Compile-time interface checks.
var (
	_ memory.Provider                 = (*Fake)(nil)
	_ memory.InboxAppender            = (*Fake)(nil)
	_ memory.Compactor                = (*Fake)(nil)
	_ memory.Searcher                 = (*Fake)(nil)
	_ memory.Explorer                 = (*Fake)(nil)
	_ memory.ProfileStore             = (*Fake)(nil)
	_ memory.SessionManager           = (*Fake)(nil)
	_ memory.Reviewer                 = (*Fake)(nil)
	_ memory.ChangelogWriter          = (*Fake)(nil)
	_ memory.ChangelogReader          = (*Fake)(nil)
	_ memory.ConstraintStore          = (*Fake)(nil)
	_ memory.VersionedProfileStore    = (*Fake)(nil)
	_ memory.VersionedConstraintStore = (*Fake)(nil)
	_ memory.SessionSnapshotStore     = (*Fake)(nil)
	_ memory.ProfileEntryStore        = (*Fake)(nil)
	_ memory.ChangelogPageReader      = (*Fake)(nil)
	_ memory.GroupEventIngestor       = (*Fake)(nil)
	_ memory.GroupCursorCommitter     = (*Fake)(nil)
	_ memory.FactStore                = (*Fake)(nil)
	_ memory.VersionedFactStore       = (*Fake)(nil)
	_ memory.ReviewHistoryReader      = (*Fake)(nil)
)

// ---------------------------------------------------------------------------
// Provider (core)
// ---------------------------------------------------------------------------

// Name implements memory.Provider.
func (f *Fake) Name() string { return "fake" }

// Bootstrap implements memory.Provider.
func (f *Fake) Bootstrap(_ context.Context, session memory.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.bootstrapped[session.ID] {
		f.bootstrapped[session.ID] = true
		if _, ok := f.sessions[session.ID]; !ok {
			f.sessions[session.ID] = nil
		}
	}
	return nil
}

// Append implements memory.Provider.
func (f *Fake) Append(_ context.Context, session memory.Session, msgs ...ai.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[session.ID] = append(f.sessions[session.ID], msgs...)
	return nil
}

// SyncGroupEventsBefore implements GroupEventIngestor. The in-memory fake has no
// public event log, so tests provide any prior group context directly.
func (f *Fake) SyncGroupEventsBefore(context.Context, memory.Session, int64) error {
	return nil
}

// AppendGroupTurn implements GroupEventIngestor by persisting the supplied
// trigger and continuation in the same order as a real per-Agent group LCM.
func (f *Fake) AppendGroupTurn(
	ctx context.Context,
	session memory.Session,
	_ string,
	trigger ai.Message,
	continuation ...ai.Message,
) error {
	messages := make([]ai.Message, 0, len(continuation)+1)
	if trigger != nil {
		messages = append(messages, trigger)
	}
	messages = append(messages, continuation...)
	return f.Append(ctx, session, messages...)
}

// CommitGroupCursor implements GroupCursorCommitter. The fake has no event log
// cursor, so committing a successful group turn is intentionally a no-op.
func (f *Fake) CommitGroupCursor(context.Context, memory.Session, int64) error {
	return nil
}

// AppendInboxInput implements memory.InboxAppender for unit tests. Durable CAS
// semantics belong to LCM integration tests; Fake preserves the append boundary.
func (f *Fake) AppendInboxInput(ctx context.Context, session memory.Session, _ string, msg ai.Message) error {
	return f.Append(ctx, session, msg)
}

// Assemble implements memory.Provider.
func (f *Fake) Assemble(_ context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	msgs := f.sessions[session.ID]
	if len(msgs) == 0 {
		return nil, nil
	}

	// Always include at least freshTail messages from the end.
	tailStart := max(len(msgs)-freshTail, 0)

	// Walk backwards from tailStart to fill budget with older messages.
	tail := msgs[tailStart:]
	tailTokens := 0
	for _, m := range tail {
		tailTokens += memory.EstimateTokens(memory.MessageText(m))
	}

	remaining := budget - tailTokens
	var prefix []ai.Message
	for i := tailStart - 1; i >= 0 && remaining > 0; i-- {
		tokens := memory.EstimateTokens(memory.MessageText(msgs[i]))
		if tokens > remaining {
			break
		}
		prefix = append([]ai.Message{msgs[i]}, prefix...)
		remaining -= tokens
	}

	return append(prefix, tail...), nil
}

// Stats implements memory.Provider.
func (f *Fake) Stats(_ context.Context, session memory.Session) (memory.SessionStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	msgs := f.sessions[session.ID]
	if len(msgs) == 0 {
		return memory.SessionStats{}, nil
	}

	var stats memory.SessionStats
	stats.MessageCount = len(msgs)
	for _, m := range msgs {
		stats.TokenCount += memory.EstimateTokens(memory.MessageText(m))
	}
	stats.SummaryCount = len(f.summaries)

	if len(msgs) > 0 {
		stats.OldestAt = memory.MessageTimestamp(msgs[0])
		stats.NewestAt = memory.MessageTimestamp(msgs[len(msgs)-1])
	}

	return stats, nil
}

// Close implements memory.Provider.
func (f *Fake) Close() error { return nil }

// ---------------------------------------------------------------------------
// Compactor
// ---------------------------------------------------------------------------

// NeedsCompaction implements memory.Compactor.
func (f *Fake) NeedsCompaction(_ context.Context, session memory.Session, threshold float64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := f.sessions[session.ID]
	// Simple heuristic: need compaction if message count exceeds threshold * 100.
	return float64(len(msgs)) > threshold*100
}

// Compact implements memory.Compactor.
func (f *Fake) Compact(_ context.Context, _ memory.Session, _ memory.CompactionMode) (*memory.CompactionResult, error) {
	// The fake does not actually compact; it returns a zero result.
	return &memory.CompactionResult{}, nil
}

// ---------------------------------------------------------------------------
// Searcher
// ---------------------------------------------------------------------------

// Search implements memory.Searcher.
func (f *Fake) Search(_ context.Context, session memory.Session, query memory.SearchQuery) ([]memory.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}

	var results []memory.SearchResult
	msgs := f.sessions[session.ID]
	for i, m := range msgs {
		if len(results) >= limit {
			break
		}
		text := memory.MessageText(m)
		if strings.Contains(strings.ToLower(text), strings.ToLower(query.Text)) {
			results = append(results, memory.SearchResult{
				SourceType: "message",
				SourceID:   fmt.Sprintf("msg_%d", i),
				Content:    truncate(text, 500),
				Score:      0,
				OccurredAt: memory.MessageTimestamp(m),
				SessionID:  session.ID,
			})
		}
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Explorer
// ---------------------------------------------------------------------------

// AddSummary adds a fake summary for testing Explorer capability.
func (f *Fake) AddSummary(s FakeSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[s.ID] = s
}

// Describe implements memory.Explorer.
func (f *Fake) Describe(_ context.Context, summaryID string) (*memory.DescribeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s, ok := f.summaries[summaryID]
	if !ok {
		return nil, fmt.Errorf("summary %q not found", summaryID)
	}

	return &memory.DescribeResult{
		SummaryID:       s.ID,
		Kind:            s.Kind,
		Depth:           s.Depth,
		Content:         s.Content,
		EarliestAt:      s.EarliestAt,
		LatestAt:        s.LatestAt,
		DescendantCount: s.DescendantCount,
		ParentIDs:       s.ParentIDs,
		ChildIDs:        s.ChildIDs,
	}, nil
}

// Expand implements memory.Explorer.
func (f *Fake) Expand(_ context.Context, summaryID string, _ int) (*memory.ExpandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s, ok := f.summaries[summaryID]
	if !ok {
		return nil, fmt.Errorf("summary %q not found", summaryID)
	}

	return &memory.ExpandResult{
		SummaryID: s.ID,
		Messages:  s.SourceMessages,
		Children:  s.ChildSummaries,
	}, nil
}

// ---------------------------------------------------------------------------
// ProfileStore
// ---------------------------------------------------------------------------

func profileKey(userID string, agentID string) string {
	return fmt.Sprintf("%s:%s", userID, agentID)
}

// GetProfile implements memory.ProfileStore.
func (f *Fake) GetProfile(_ context.Context, userID string, agentID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles[profileKey(userID, agentID)], nil
}

// SetProfile implements memory.ProfileStore.
func (f *Fake) SetProfile(_ context.Context, userID string, agentID string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[profileKey(userID, agentID)] = content
	return nil
}

// GetAgentSoul implements memory.ProfileStore.
func (f *Fake) GetAgentSoul(_ context.Context, userID string, agentID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.souls[profileKey(userID, agentID)], nil
}

// SetAgentSoul implements memory.ProfileStore.
func (f *Fake) SetAgentSoul(_ context.Context, userID string, agentID string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.souls[profileKey(userID, agentID)] = content
	return nil
}

// ---------------------------------------------------------------------------
// FactStore
// ---------------------------------------------------------------------------

// ListActiveFacts implements memory.FactStore.
func (f *Fake) ListActiveFacts(_ context.Context, userID string, agentID string, subject memory.FactSubject) ([]memory.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listActiveFactsLocked(userID, agentID, subject), nil
}

// ListActiveFactsAt implements memory.VersionedFactStore.
// The fake ignores version and returns the current facts, which is sufficient
// for prompt unit tests that only need the interface shape.
func (f *Fake) ListActiveFactsAt(_ context.Context, userID string, agentID string, subject memory.FactSubject, _ int64) ([]memory.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listActiveFactsLocked(userID, agentID, subject), nil
}

// AddFact is a test helper to populate active facts.
func (f *Fake) AddFact(userID string, agentID string, fact memory.Fact) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fact.Status == "" {
		fact.Status = memory.FactStatusActive
	}
	if fact.Scope == "" {
		fact.Scope = "user_agent"
	}
	f.facts[profileKey(userID, agentID)] = append(f.facts[profileKey(userID, agentID)], fact)
}

func (f *Fake) listActiveFactsLocked(userID string, agentID string, subject memory.FactSubject) []memory.Fact {
	facts := f.facts[profileKey(userID, agentID)]
	out := make([]memory.Fact, 0, len(facts))
	for _, fact := range facts {
		if fact.Subject == subject && fact.Status == memory.FactStatusActive {
			out = append(out, fact)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ConstraintStore
// ---------------------------------------------------------------------------

// GetConstraints implements memory.ConstraintStore.
func (f *Fake) GetConstraints(_ context.Context, userID string, agentID string) ([]memory.ConstraintEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	if cs, ok := f.constraints[key]; ok {
		out := make([]memory.ConstraintEntry, len(cs))
		copy(out, cs)
		return out, nil
	}
	return []memory.ConstraintEntry{}, nil
}

// AddConstraint implements memory.ConstraintStore.
func (f *Fake) AddConstraint(_ context.Context, userID string, agentID string, text string) ([]memory.ConstraintEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	entry := memory.ConstraintEntry{
		ID:        fmt.Sprintf("c%d", len(f.constraints[key])+1),
		Text:      text,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	f.constraints[key] = append(f.constraints[key], entry)
	out := make([]memory.ConstraintEntry, len(f.constraints[key]))
	copy(out, f.constraints[key])
	return out, nil
}

// RemoveConstraint implements memory.ConstraintStore.
func (f *Fake) RemoveConstraint(_ context.Context, userID string, agentID string, id string) ([]memory.ConstraintEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	existing := f.constraints[key]
	updated := make([]memory.ConstraintEntry, 0, len(existing))
	for _, c := range existing {
		if c.ID != id {
			updated = append(updated, c)
		}
	}
	f.constraints[key] = updated
	out := make([]memory.ConstraintEntry, len(updated))
	copy(out, updated)
	return out, nil
}

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

// SaveInfo implements memory.SessionManager.
func (f *Fake) SaveInfo(_ context.Context, info memory.SessionInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.sessionInfos[info.ID]; ok {
		if existing.info.Archived {
			return fmt.Errorf("%w: %s", memory.ErrInactiveSession, info.ID)
		}
		// Existing lifecycle state is not generic metadata. Tests using the fake
		// observe the same one-way transition contract as the SQL provider.
		info.Archived = false
	}
	f.sessionInfos[info.ID] = fakeSessionInfo{info: info}
	return nil
}

// ArchiveInfo implements memory.SessionManager.
func (f *Fake) ArchiveInfo(_ context.Context, info memory.SessionInfo) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.sessionInfos[info.ID]
	if !ok || existing.info.Archived || existing.info.UserID != info.UserID || existing.info.AgentID != info.AgentID {
		return false, nil
	}
	existing.info.Archived = true
	f.sessionInfos[info.ID] = existing
	return true, nil
}

// TouchActiveInfo implements memory.SessionManager. The mutex stands in for the
// real provider's single guarded UPDATE: an archived (or missing) row is never
// written and never resurrected.
func (f *Fake) TouchActiveInfo(_ context.Context, info memory.SessionInfo) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	si, ok := f.sessionInfos[info.ID]
	if !ok || si.info.Archived {
		return false, nil
	}
	if si.info.Title == "" {
		si.info.Title = info.Title
	}
	if si.info.Channel == "" {
		si.info.Channel = info.Channel
	}
	if si.info.GroupID == "" {
		si.info.GroupID = info.GroupID
	}
	si.info.LastActive = time.Now().UTC()
	f.sessionInfos[info.ID] = si
	return true, nil
}

// MarkSessionTurnStarted implements memory.SessionActivityStore.
func (f *Fake) MarkSessionTurnStarted(_ context.Context, session memory.Session) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	si, ok := f.sessionInfos[session.ID]
	if !ok || si.info.Archived || si.info.UserID != session.UserID || si.info.AgentID != session.AgentID {
		return false, nil
	}
	si.info.LastTurnStartedAt = time.Now().UTC()
	f.sessionInfos[session.ID] = si
	return true, nil
}

// MarkSessionTurnCompleted implements memory.SessionActivityStore.
func (f *Fake) MarkSessionTurnCompleted(_ context.Context, session memory.Session, result memory.SessionTurnResult) (bool, error) {
	if !result.Valid() {
		return false, fmt.Errorf("invalid session turn result %q", result)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	si, ok := f.sessionInfos[session.ID]
	if !ok || si.info.Archived || si.info.UserID != session.UserID || si.info.AgentID != session.AgentID {
		return false, nil
	}
	si.info.LastTurnCompletedAt = time.Now().UTC()
	si.info.LastTurnResult = result
	f.sessionInfos[session.ID] = si
	return true, nil
}

// MarkSessionViewed implements memory.SessionActivityStore.
func (f *Fake) MarkSessionViewed(_ context.Context, session memory.Session) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	si, ok := f.sessionInfos[session.ID]
	if !ok || si.info.UserID != session.UserID || si.info.AgentID != session.AgentID {
		return false, nil
	}
	si.info.LastViewedAt = time.Now().UTC()
	f.sessionInfos[session.ID] = si
	return true, nil
}

// RotateInfo implements memory.SessionManager. The mutex stands in for the real
// provider's transaction: the archive and the successor become visible together.
func (f *Fake) RotateInfo(_ context.Context, expectedSessionID string, successor memory.SessionInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rotateErr != nil {
		return f.rotateErr
	}
	expected, ok := f.sessionInfos[expectedSessionID]
	if !ok || expected.info.Archived || expected.info.Kind != successor.Kind ||
		expected.info.UserID != successor.UserID || expected.info.AgentID != successor.AgentID ||
		expected.info.ProjectID != successor.ProjectID {
		return fmt.Errorf("%w: %s", memory.ErrStaleRotation, expectedSessionID)
	}
	expected.info.Archived = true
	f.sessionInfos[expectedSessionID] = expected
	f.sessionInfos[successor.ID] = fakeSessionInfo{info: successor}
	return nil
}

// FailRotation makes the next RotateInfo calls fail with err, so callers can
// prove a rotation failure leaves the predecessor untouched.
func (f *Fake) FailRotation(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotateErr = err
}

// LoadInfo implements memory.SessionManager.
func (f *Fake) LoadInfo(_ context.Context, sessionID string) (memory.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	si, ok := f.sessionInfos[sessionID]
	if !ok {
		return memory.SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
	}
	return si.info, nil
}

// ListInfo implements memory.SessionManager.
func (f *Fake) ListInfo(_ context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var results []memory.SessionInfo
	for _, si := range f.sessionInfos {
		if opts.AgentID != "" && si.info.AgentID != opts.AgentID {
			continue
		}
		if opts.UserID != "" && si.info.UserID != opts.UserID {
			continue
		}
		if opts.Kind != "" && si.info.Kind != opts.Kind {
			continue
		}
		if opts.Channel != "" && si.info.Channel != opts.Channel {
			continue
		}
		if opts.ExcludeInternal && (si.info.Kind == "task" || si.info.Kind == "delegate") {
			continue
		}
		if opts.ProjectIDIsNull && si.info.ProjectID != "" {
			continue
		}
		if opts.ProjectID != "" && si.info.ProjectID != opts.ProjectID {
			continue
		}
		if !opts.IncludeArchived && si.info.Archived {
			continue
		}
		results = append(results, si.info)
	}

	// Sort by LastActive descending for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		if results[i].LastActive.Equal(results[j].LastActive) {
			return results[i].ID > results[j].ID
		}
		return results[i].LastActive.After(results[j].LastActive)
	})

	if opts.Offset > 0 {
		if opts.Offset >= len(results) {
			return nil, nil
		}
		results = results[opts.Offset:]
	}
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

// LoadHistory implements memory.SessionManager.
func (f *Fake) LoadHistory(_ context.Context, sessionID string) ([]ai.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, ok := f.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	// Return a copy to prevent mutation.
	out := make([]ai.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// LoadReviewHistory implements memory.ReviewHistoryReader with one stable
// storage boundary per appended message.
func (f *Fake) LoadReviewHistory(_ context.Context, sessionID string) ([]memory.ReviewMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, ok := f.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	out := make([]memory.ReviewMessage, len(msgs))
	for index, msg := range msgs {
		seq := int64(index + 1)
		out[index] = memory.ReviewMessage{
			ID:       fmt.Sprintf("%s:%d", sessionID, seq),
			FirstSeq: seq,
			LastSeq:  seq,
			Message:  msg,
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reviewer
// ---------------------------------------------------------------------------

// BuildReviewContext implements memory.Reviewer.
func (f *Fake) BuildReviewContext(_ context.Context, session memory.Session, since time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	msgs := f.sessions[session.ID]
	if len(msgs) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, m := range msgs {
		ts := memory.MessageTimestamp(m)
		if !since.IsZero() && ts.Before(since) {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n", memory.MessageRole(m), memory.MessageText(m))
	}

	return b.String(), nil
}

// ---------------------------------------------------------------------------
// ChangelogWriter / ChangelogReader
// ---------------------------------------------------------------------------

// WriteChangelog implements memory.ChangelogWriter.
func (f *Fake) WriteChangelog(_ context.Context, entry memory.ChangeEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changelog = append(f.changelog, entry)
	return nil
}

// ReadChangelog implements memory.ChangelogReader.
// Returns up to limit entries for the given scope in reverse-insertion order.
func (f *Fake) ReadChangelog(_ context.Context, userID string, agentID string, scope string, limit int) ([]memory.ChangeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []memory.ChangeEntry
	for i := len(f.changelog) - 1; i >= 0 && len(result) < limit; i-- {
		e := f.changelog[i]
		if e.UserID == userID && e.AgentID == agentID && e.Scope == scope {
			result = append(result, e)
		}
	}
	return result, nil
}

// ReadChangelogPage implements stable keyset pagination for server tests.
func (f *Fake) ReadChangelogPage(_ context.Context, userID string, agentID string, scope string, cursor *memory.ChangelogCursor, limit int) ([]memory.ChangeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		return nil, fmt.Errorf("changelog page limit must be positive")
	}
	if cursor != nil && (cursor.CreatedAt.IsZero() || cursor.ID == "") {
		return nil, fmt.Errorf("changelog cursor created_at and id are required")
	}

	result := make([]memory.ChangeEntry, 0, len(f.changelog))
	for _, entry := range f.changelog {
		if entry.UserID == userID && entry.AgentID == agentID && entry.Scope == scope {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, result[i].CreatedAt)
		right, _ := time.Parse(time.RFC3339Nano, result[j].CreatedAt)
		if left.Equal(right) {
			return result[i].ID > result[j].ID
		}
		return left.After(right)
	})
	if cursor != nil {
		filtered := result[:0]
		for _, entry := range result {
			createdAt, _ := time.Parse(time.RFC3339Nano, entry.CreatedAt)
			if createdAt.Before(cursor.CreatedAt) || (createdAt.Equal(cursor.CreatedAt) && entry.ID < cursor.ID) {
				filtered = append(filtered, entry)
			}
		}
		result = filtered
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Changelog returns all recorded changelog entries (in insertion order) for test assertions.
func (f *Fake) Changelog() []memory.ChangeEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]memory.ChangeEntry, len(f.changelog))
	copy(out, f.changelog)
	return out
}

// ---------------------------------------------------------------------------
// VersionedProfileStore
// ---------------------------------------------------------------------------

// GetProfileAt implements memory.VersionedProfileStore.
// The fake ignores version and returns the current profile (sufficient for tests).
func (f *Fake) GetProfileAt(_ context.Context, userID string, agentID string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles[profileKey(userID, agentID)], nil
}

// GetAgentSoulAt implements memory.VersionedProfileStore.
// The fake ignores version and returns the current soul (sufficient for tests).
func (f *Fake) GetAgentSoulAt(_ context.Context, userID string, agentID string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.souls[profileKey(userID, agentID)], nil
}

// ---------------------------------------------------------------------------
// VersionedConstraintStore
// ---------------------------------------------------------------------------

// GetConstraintsAt implements memory.VersionedConstraintStore.
// The fake ignores version and returns the current constraints (sufficient for tests).
func (f *Fake) GetConstraintsAt(_ context.Context, userID string, agentID string, _ int64) ([]memory.ConstraintEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	if cs, ok := f.constraints[key]; ok {
		out := make([]memory.ConstraintEntry, len(cs))
		copy(out, cs)
		return out, nil
	}
	return []memory.ConstraintEntry{}, nil
}

// ---------------------------------------------------------------------------
// SessionSnapshotStore
// ---------------------------------------------------------------------------

func snapshotKey(sessionID string, userID string, agentID string) string {
	return fmt.Sprintf("%s:%s:%s", sessionID, userID, agentID)
}

// GetOrCreateSessionSnapshot implements memory.SessionSnapshotStore.
// Creates with version 0 when not found (sufficient for tests).
func (f *Fake) GetOrCreateSessionSnapshot(_ context.Context, sessionID string, userID string, agentID string) (memory.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := snapshotKey(sessionID, userID, agentID)
	if snap, ok := f.snapshots[key]; ok {
		return snap, nil
	}
	snap := memory.SessionSnapshot{
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
		Version:   0,
		UpdatedAt: time.Now(),
	}
	f.snapshots[key] = snap
	return snap, nil
}

// AdvanceSessionSnapshot implements memory.SessionSnapshotStore.
// Updates the snapshot version to match the number of changelog entries.
func (f *Fake) AdvanceSessionSnapshot(_ context.Context, sessionID string, userID string, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := snapshotKey(sessionID, userID, agentID)
	snap, ok := f.snapshots[key]
	if !ok {
		return nil
	}
	// Advance version by counting changelog entries for this user/agent.
	var count int64
	for _, e := range f.changelog {
		if e.UserID == userID && e.AgentID == agentID {
			count++
		}
	}
	snap.Version = count
	snap.UpdatedAt = time.Now()
	f.snapshots[key] = snap
	return nil
}

// Snapshots returns all stored snapshots for test assertions.
func (f *Fake) Snapshots() map[string]memory.SessionSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]memory.SessionSnapshot, len(f.snapshots))
	maps.Copy(out, f.snapshots)
	return out
}

// ---------------------------------------------------------------------------
// ProfileEntryStore
// ---------------------------------------------------------------------------

// GetProfileEntries implements memory.ProfileEntryStore.
func (f *Fake) GetProfileEntries(_ context.Context, userID string, agentID string) ([]memory.ProfileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	if entries, ok := f.profileEntries[key]; ok {
		out := make([]memory.ProfileEntry, len(entries))
		copy(out, entries)
		return out, nil
	}
	return []memory.ProfileEntry{}, nil
}

// AddProfileEntry is a test helper to populate profile entries.
func (f *Fake) AddProfileEntry(userID, agentID string, entry memory.ProfileEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profileKey(userID, agentID)
	f.profileEntries[key] = append(f.profileEntries[key], entry)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}
