// External tests for traced.go using memorytest.Fake.
package memory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type memoryContextKey string

// collectingHook records every PostMemoryCall invocation.
type collectingHook struct {
	events []*hooks.PostMemoryCallContext
}

func (c *collectingHook) Name() string  { return "collector" }
func (c *collectingHook) Priority() int { return 1 }
func (c *collectingHook) OnPostMemoryCall(_ context.Context, hctx *hooks.PostMemoryCallContext) {
	copied := *hctx
	c.events = append(c.events, &copied)
}

type contextInjectingHook struct {
	key memoryContextKey
}

func (h contextInjectingHook) Name() string  { return "inject" }
func (h contextInjectingHook) Priority() int { return 0 }
func (h contextInjectingHook) OnPreMemoryCall(ctx context.Context, _ *hooks.PreMemoryCallContext) (hooks.PreMemoryCallResult, error) {
	return hooks.PreMemoryCallResult{Context: context.WithValue(ctx, h.key, "memory-span")}, nil
}
func (h contextInjectingHook) OnPostMemoryCall(context.Context, *hooks.PostMemoryCallContext) {}

func newTracedWithCollector(inner memory.Provider) (memory.Provider, *collectingHook) {
	col := &collectingHook{}
	hs := hooks.NewHookSet([]hooks.HookPlugin{col})
	traced := memory.WithTracing(inner, func() *hooks.HookSet { return hs })
	return traced, col
}

type contextCheckingProvider struct {
	memory.Provider
	key memoryContextKey
	t   *testing.T
}

type reviewHistoryProvider struct {
	memory.Provider
	messages []memory.ReviewMessage
}

type groupCursorProvider struct {
	memory.Provider
	committed int64
}

func (p *groupCursorProvider) CommitGroupCursor(_ context.Context, _ memory.Session, triggerSeq int64) error {
	p.committed = triggerSeq
	return nil
}

func (p *reviewHistoryProvider) LoadReviewHistory(context.Context, string) ([]memory.ReviewMessage, error) {
	return append([]memory.ReviewMessage(nil), p.messages...), nil
}

func (p *contextCheckingProvider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	p.t.Helper()
	if got := ctx.Value(p.key); got != "memory-span" {
		p.t.Fatalf("inner provider saw context value %#v, want memory-span", got)
	}
	return p.Provider.Append(ctx, session, msgs...)
}

var testSession = memory.Session{ID: "sess-1", AgentID: "agent-1", UserID: "42"}

func TestTracedProvider_Name(t *testing.T) {
	fake := memorytest.New()
	traced, _ := newTracedWithCollector(fake)
	if traced.Name() != "fake" {
		t.Errorf("expected 'fake', got %q", traced.Name())
	}
}

func TestTracedProvider_Unwrap(t *testing.T) {
	fake := memorytest.New()
	traced := memory.WithTracing(fake, nil)
	inner := memory.Unwrap(traced)
	if inner != fake {
		t.Error("Unwrap should return inner provider")
	}
}

func TestTracedProvider_ForwardsGroupCursorCommit(t *testing.T) {
	inner := &groupCursorProvider{Provider: memorytest.New()}
	traced := memory.WithTracing(inner, nil)
	committer, ok := traced.(memory.GroupCursorCommitter)
	if !ok {
		t.Fatal("traced provider does not expose GroupCursorCommitter")
	}
	if err := committer.CommitGroupCursor(context.Background(), testSession, 42); err != nil {
		t.Fatalf("commit group cursor: %v", err)
	}
	if inner.committed != 42 {
		t.Fatalf("inner committed seq = %d, want 42", inner.committed)
	}
}

func TestUnwrap_NonWrapped(t *testing.T) {
	fake := memorytest.New()
	got := memory.Unwrap(fake)
	if got != fake {
		t.Error("Unwrap of non-wrapped should return itself")
	}
}

func TestTracedProvider_Bootstrap(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	err := traced.Bootstrap(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(col.events))
	}
	if col.events[0].Op != hooks.MemoryOpBootstrap {
		t.Errorf("expected Bootstrap op, got %q", col.events[0].Op)
	}
}

func TestTracedProvider_GroupSessionHookDoesNotExposeStorageOwnerAsUser(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	groupID := "11111111-1111-4111-8111-111111111111"
	groupSession := memory.Session{
		ID:      "group-session-1",
		AgentID: "agent-1",
		UserID:  groupID,
		GroupID: groupID,
	}

	if err := traced.Bootstrap(context.Background(), groupSession); err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(col.events))
	}
	got := col.events[0]
	if got.UserID != "" {
		t.Fatalf("group hook UserID = %q, want empty", got.UserID)
	}
	if got.SessionID != groupSession.ID || got.AgentID != groupSession.AgentID {
		t.Fatalf("group hook metadata = %+v, want session=%q agent=%q", got.HookMeta, groupSession.ID, groupSession.AgentID)
	}
}

func TestTracedProvider_PreMemoryContextReachesInnerProvider(t *testing.T) {
	key := memoryContextKey("trace")
	inner := &contextCheckingProvider{Provider: memorytest.New(), key: key, t: t}
	traced := memory.WithTracing(inner, func() *hooks.HookSet {
		return hooks.NewHookSet([]hooks.HookPlugin{contextInjectingHook{key: key}})
	})

	if err := traced.Append(context.Background(), testSession, ai.UserMessage{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
}

func TestTracedProvider_Append(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	col.events = nil

	msg := ai.UserMessage{Content: "hello"}
	err := traced.Append(context.Background(), testSession, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(col.events))
	}
	if col.events[0].Op != hooks.MemoryOpAppend {
		t.Errorf("expected Append op, got %q", col.events[0].Op)
	}
	if col.events[0].MessageCount != 1 {
		t.Errorf("expected MessageCount=1, got %d", col.events[0].MessageCount)
	}
}

func TestTracedProvider_Assemble(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	_ = traced.Append(context.Background(), testSession, ai.UserMessage{Content: "hi"})
	col.events = nil

	msgs, err := traced.Assemble(context.Background(), testSession, 10000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Error("expected at least one assembled message")
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpAssemble {
		t.Errorf("expected Assemble event, got %v", col.events)
	}
}

func TestTracedProvider_Stats(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	col.events = nil

	_, err := traced.Stats(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpStats {
		t.Errorf("expected Stats event, got %v", col.events)
	}
}

func TestTracedProvider_Close(t *testing.T) {
	fake := memorytest.New()
	traced, _ := newTracedWithCollector(fake)
	if err := traced.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTracedProvider_NeedsCompaction(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	col.events = nil

	got := traced.(memory.Compactor).NeedsCompaction(context.Background(), testSession, 0.5)
	if got {
		t.Error("expected false for empty session")
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpNeedsCompaction {
		t.Errorf("expected NeedsCompaction event, got %v", col.events)
	}
}

func TestTracedProvider_Compact(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	col.events = nil

	result, err := traced.(memory.Compactor).Compact(context.Background(), testSession, memory.CompactionIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpCompact {
		t.Errorf("expected Compact event, got %v", col.events)
	}
}

func TestTracedProvider_Search(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	_ = traced.Append(context.Background(), testSession, ai.UserMessage{Content: "hello world"})
	col.events = nil

	results, err := traced.(memory.Searcher).Search(context.Background(), testSession, memory.SearchQuery{Text: "hello", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected at least one search result")
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpSearch {
		t.Errorf("expected Search event, got %v", col.events)
	}
}

func TestTracedProvider_Describe(t *testing.T) {
	fake := memorytest.New()
	fake.AddSummary(memorytest.FakeSummary{
		ID:      "sum-1",
		Kind:    "leaf",
		Content: "summary content",
	})
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	result, err := traced.(memory.Explorer).Describe(context.Background(), "sum-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryID != "sum-1" {
		t.Errorf("unexpected summary ID: %q", result.SummaryID)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpDescribe {
		t.Errorf("expected Describe event, got %v", col.events)
	}
}

func TestTracedProvider_Expand(t *testing.T) {
	fake := memorytest.New()
	fake.AddSummary(memorytest.FakeSummary{
		ID:   "sum-1",
		Kind: "leaf",
	})
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	result, err := traced.(memory.Explorer).Expand(context.Background(), "sum-1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpExpand {
		t.Errorf("expected Expand event, got %v", col.events)
	}
}

func TestTracedProvider_GetProfile(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	_, err := traced.(memory.ProfileStore).GetProfile(context.Background(), "1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpGetProfile {
		t.Errorf("expected GetProfile event, got %v", col.events)
	}
}

func TestTracedProvider_GetProfileAt(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	_, err := traced.(memory.VersionedProfileStore).GetProfileAt(context.Background(), "1", "agent-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpGetProfileAt {
		t.Errorf("expected GetProfileAt event, got %v", col.events)
	}
}

func TestTracedProvider_SetProfile(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	err := traced.(memory.ProfileStore).SetProfile(context.Background(), "1", "agent-1", "profile content")
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpSetProfile {
		t.Errorf("expected SetProfile event, got %v", col.events)
	}
}

func TestTracedProvider_GetAgentSoul(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	_, err := traced.(memory.ProfileStore).GetAgentSoul(context.Background(), "1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpGetAgentSoul {
		t.Errorf("expected GetAgentSoul event, got %v", col.events)
	}
}

func TestTracedProvider_GetAgentSoulAt(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	_, err := traced.(memory.VersionedProfileStore).GetAgentSoulAt(context.Background(), "1", "agent-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpGetAgentSoulAt {
		t.Errorf("expected GetAgentSoulAt event, got %v", col.events)
	}
}

func TestTracedProvider_GetOrCreateSessionSnapshotReportsVersionZero(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	snapshot, err := traced.(memory.SessionSnapshotStore).GetOrCreateSessionSnapshot(
		context.Background(), testSession.ID, testSession.UserID, testSession.AgentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 0 {
		t.Fatalf("snapshot version = %d, want 0", snapshot.Version)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpGetOrCreateSessionSnapshot {
		t.Fatalf("expected GetOrCreateSessionSnapshot event, got %v", col.events)
	}
	if !strings.Contains(col.events[0].Detail, "version=0 ") {
		t.Fatalf("expected version-zero trace detail, got %q", col.events[0].Detail)
	}
}

func TestTracedProvider_SetAgentSoul(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	err := traced.(memory.ProfileStore).SetAgentSoul(context.Background(), "1", "agent-1", "soul content")
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpSetAgentSoul {
		t.Errorf("expected SetAgentSoul event, got %v", col.events)
	}
}

func TestTracedProvider_ReadChangelogPage(t *testing.T) {
	fake := memorytest.New()
	entry := memory.ChangeEntry{
		ID: "change-1", UserID: "1", AgentID: "agent-1", Scope: "constraint",
		Action: "create", CreatedAt: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	if err := fake.WriteChangelog(context.Background(), entry); err != nil {
		t.Fatalf("write fake changelog: %v", err)
	}
	traced, _ := newTracedWithCollector(fake)
	rows, err := traced.(memory.ChangelogPageReader).ReadChangelogPage(context.Background(), "1", "agent-1", "constraint", nil, 1)
	if err != nil {
		t.Fatalf("read traced changelog page: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != entry.ID {
		t.Fatalf("traced changelog page = %+v, want forwarded entry", rows)
	}
}

func TestTracedProvider_SaveInfo(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	info := memory.SessionInfo{ID: "sess-1", AgentID: "agent-1", UserID: "1", Title: "Test Session"}
	err := traced.(memory.SessionManager).SaveInfo(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpSaveInfo {
		t.Errorf("expected SaveInfo event, got %v", col.events)
	}
}

func TestTracedProvider_SaveGroupInfoHookDoesNotExposeStorageOwnerAsUser(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	groupID := "11111111-1111-4111-8111-111111111111"
	info := memory.SessionInfo{
		ID:      "group-session-1",
		AgentID: "agent-1",
		UserID:  groupID,
		GroupID: groupID,
	}

	if err := traced.(memory.SessionManager).SaveInfo(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(col.events))
	}
	got := col.events[0]
	if got.UserID != "" {
		t.Fatalf("group SaveInfo hook UserID = %q, want empty", got.UserID)
	}
	if got.SessionID != info.ID || got.AgentID != info.AgentID {
		t.Fatalf("group SaveInfo hook metadata = %+v, want session=%q agent=%q", got.HookMeta, info.ID, info.AgentID)
	}
}

func TestTracedProvider_LoadInfo(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	info := memory.SessionInfo{ID: "sess-1", AgentID: "agent-1", UserID: "1"}
	_ = traced.(memory.SessionManager).SaveInfo(context.Background(), info)
	col.events = nil

	_, err := traced.(memory.SessionManager).LoadInfo(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpLoadInfo {
		t.Errorf("expected LoadInfo event, got %v", col.events)
	}
}

func TestTracedProvider_ListInfo(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)
	col.events = nil

	_, err := traced.(memory.SessionManager).ListInfo(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpListInfo {
		t.Errorf("expected ListInfo event, got %v", col.events)
	}
}

// reviewListerProvider augments Fake with the optional ListInfoForReview
// capability, mirroring what the LCM provider exposes.
type reviewListerProvider struct {
	memory.Provider
	called bool
}

func (p *reviewListerProvider) ListInfoForReview(_ context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	p.called = true
	return []memory.SessionInfo{{ID: "sess-1", AgentID: opts.AgentID}}, nil
}

func TestTracedProvider_ListInfoForReview(t *testing.T) {
	inner := &reviewListerProvider{Provider: memorytest.New()}
	traced, col := newTracedWithCollector(inner)
	col.events = nil

	// The session adapter discovers this capability via type assertion; the
	// wrapper must expose it or callers fall back to ListInfo (user-scoped).
	lister, ok := traced.(interface {
		ListInfoForReview(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error)
	})
	if !ok {
		t.Fatal("traced provider must expose ListInfoForReview")
	}

	infos, err := lister.ListInfoForReview(context.Background(), memory.ListOptions{AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !inner.called {
		t.Error("expected call to forward to inner provider")
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpListInfoForReview {
		t.Errorf("expected ListInfoForReview event, got %v", col.events)
	}
}

func TestTracedProvider_LoadHistory(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	_ = traced.Append(context.Background(), testSession, ai.UserMessage{Content: "msg"})
	col.events = nil

	_, err := traced.(memory.SessionManager).LoadHistory(context.Background(), testSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpLoadHistory {
		t.Errorf("expected LoadHistory event, got %v", col.events)
	}
}

func TestTracedProvider_LoadReviewHistory(t *testing.T) {
	inner := &reviewHistoryProvider{
		Provider: memorytest.New(),
		messages: []memory.ReviewMessage{{
			ID: "message-1", FirstSeq: 1, LastSeq: 1,
			Message: ai.UserMessage{Content: "durable preference"},
		}},
	}
	traced, col := newTracedWithCollector(inner)

	reader, ok := traced.(memory.ReviewHistoryReader)
	if !ok {
		t.Fatal("traced provider does not expose ReviewHistoryReader")
	}
	msgs, err := reader.LoadReviewHistory(context.Background(), testSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].LastSeq != 1 {
		t.Fatalf("review history = %#v, want stable seq boundary", msgs)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpLoadReviewHistory {
		t.Fatalf("events = %#v, want LoadReviewHistory event", col.events)
	}
}

func TestTracedProvider_BuildReviewContext(t *testing.T) {
	fake := memorytest.New()
	traced, col := newTracedWithCollector(fake)

	_ = traced.Bootstrap(context.Background(), testSession)
	_ = traced.Append(context.Background(), testSession, ai.UserMessage{Content: "hello"})
	col.events = nil

	_, err := traced.(memory.Reviewer).BuildReviewContext(context.Background(), testSession, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(col.events) != 1 || col.events[0].Op != hooks.MemoryOpBuildReview {
		t.Errorf("expected BuildReview event, got %v", col.events)
	}
}

func TestTracedProvider_NilHookSet(t *testing.T) {
	// WithTracing with nil hooksFn should not panic and still delegate.
	fake := memorytest.New()
	traced := memory.WithTracing(fake, nil)

	if err := traced.Bootstrap(context.Background(), testSession); err != nil {
		t.Fatal(err)
	}
}

func TestTracedProvider_Expand_WithMessages(t *testing.T) {
	// Summary with more than 5 source messages to hit the truncation branch.
	fake := memorytest.New()
	msgs := make([]memory.ExpandMessage, 7)
	for i := range msgs {
		msgs[i] = memory.ExpandMessage{Role: "user", Content: "msg"}
	}
	fake.AddSummary(memorytest.FakeSummary{
		ID:             "sum-big",
		Kind:           "leaf",
		SourceMessages: msgs,
	})
	traced, _ := newTracedWithCollector(fake)

	result, err := traced.(memory.Explorer).Expand(context.Background(), "sum-big", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Messages) != 7 {
		t.Errorf("expected 7 messages in result, got %v", result)
	}
}

func TestTracedProvider_Expand_WithChildren(t *testing.T) {
	// Condensed summary with more than 5 children.
	fake := memorytest.New()
	children := make([]memory.ExpandChild, 7)
	for i := range children {
		children[i] = memory.ExpandChild{Kind: "leaf", Content: "child summary"}
	}
	fake.AddSummary(memorytest.FakeSummary{
		ID:             "sum-condensed",
		Kind:           "condensed",
		ChildSummaries: children,
	})
	traced, _ := newTracedWithCollector(fake)

	result, err := traced.(memory.Explorer).Expand(context.Background(), "sum-condensed", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Children) != 7 {
		t.Errorf("expected 7 children in result, got %v", result)
	}
}
