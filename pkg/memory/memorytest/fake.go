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

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
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
	// mu protects all maps.
	mu sync.Mutex
}

// New creates a new Fake provider ready for use.
func New() *Fake {
	return &Fake{
		sessions:     make(map[string][]ai.Message),
		profiles:     make(map[string]string),
		souls:        make(map[string]string),
		constraints:  make(map[string][]memory.ConstraintEntry),
		summaries:    make(map[string]FakeSummary),
		sessionInfos: make(map[string]fakeSessionInfo),
		bootstrapped: make(map[string]bool),
		snapshots:    make(map[string]memory.SessionSnapshot),
	}
}

// Compile-time interface checks.
var (
	_ memory.Provider                 = (*Fake)(nil)
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
				Timestamp:  memory.MessageTimestamp(m),
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

func profileKey(userID int64, agentID string) string {
	return fmt.Sprintf("%d:%s", userID, agentID)
}

// GetProfile implements memory.ProfileStore.
func (f *Fake) GetProfile(_ context.Context, userID int64, agentID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles[profileKey(userID, agentID)], nil
}

// SetProfile implements memory.ProfileStore.
func (f *Fake) SetProfile(_ context.Context, userID int64, agentID string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[profileKey(userID, agentID)] = content
	return nil
}

// GetAgentSoul implements memory.ProfileStore.
func (f *Fake) GetAgentSoul(_ context.Context, userID int64, agentID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.souls[profileKey(userID, agentID)], nil
}

// SetAgentSoul implements memory.ProfileStore.
func (f *Fake) SetAgentSoul(_ context.Context, userID int64, agentID string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.souls[profileKey(userID, agentID)] = content
	return nil
}

// ---------------------------------------------------------------------------
// ConstraintStore
// ---------------------------------------------------------------------------

// GetConstraints implements memory.ConstraintStore.
func (f *Fake) GetConstraints(_ context.Context, userID int64, agentID string) ([]memory.ConstraintEntry, error) {
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
func (f *Fake) AddConstraint(_ context.Context, userID int64, agentID string, text string) ([]memory.ConstraintEntry, error) {
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
func (f *Fake) RemoveConstraint(_ context.Context, userID int64, agentID string, id string) ([]memory.ConstraintEntry, error) {
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
	f.sessionInfos[info.ID] = fakeSessionInfo{info: info}
	return nil
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
		if opts.UserID != 0 && si.info.UserID != opts.UserID {
			continue
		}
		if !opts.IncludeArchived && si.info.Archived {
			continue
		}
		results = append(results, si.info)
	}

	// Sort by LastActive descending for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].LastActive.After(results[j].LastActive)
	})

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
func (f *Fake) ReadChangelog(_ context.Context, userID int64, agentID string, scope string, limit int) ([]memory.ChangeEntry, error) {
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
func (f *Fake) GetProfileAt(_ context.Context, userID int64, agentID string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles[profileKey(userID, agentID)], nil
}

// GetAgentSoulAt implements memory.VersionedProfileStore.
// The fake ignores version and returns the current soul (sufficient for tests).
func (f *Fake) GetAgentSoulAt(_ context.Context, userID int64, agentID string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.souls[profileKey(userID, agentID)], nil
}

// ---------------------------------------------------------------------------
// VersionedConstraintStore
// ---------------------------------------------------------------------------

// GetConstraintsAt implements memory.VersionedConstraintStore.
// The fake ignores version and returns the current constraints (sufficient for tests).
func (f *Fake) GetConstraintsAt(_ context.Context, userID int64, agentID string, _ int64) ([]memory.ConstraintEntry, error) {
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

func snapshotKey(sessionID string, userID int64, agentID string) string {
	return fmt.Sprintf("%s:%d:%s", sessionID, userID, agentID)
}

// GetOrCreateSessionSnapshot implements memory.SessionSnapshotStore.
// Creates with version 0 when not found (sufficient for tests).
func (f *Fake) GetOrCreateSessionSnapshot(_ context.Context, sessionID string, userID int64, agentID string) (memory.SessionSnapshot, error) {
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
	}
	f.snapshots[key] = snap
	return snap, nil
}

// AdvanceSessionSnapshot implements memory.SessionSnapshotStore.
// Updates the snapshot version to match the number of changelog entries.
func (f *Fake) AdvanceSessionSnapshot(_ context.Context, sessionID string, userID int64, agentID string) error {
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
// Helpers
// ---------------------------------------------------------------------------

func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}
