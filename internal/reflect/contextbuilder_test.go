package reflect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestBuildReviewContext_PrefersReviewer(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: fake, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: "hello", Timestamp: now}); err != nil {
		t.Fatal(err)
	}

	text, err := svc.buildReviewContext(ctx, sess, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("expected text to contain 'hello', got %q", text)
	}
}

func TestBuildReviewContext_FallbackWithTimestamps(t *testing.T) {
	// Use a provider that implements SessionManager but not Reviewer.
	fake := memorytest.New()

	// Wrap fake to hide the Reviewer interface.
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC().Add(-time.Hour)
	after := time.Now().UTC()

	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: "old message", Timestamp: before},
		ai.UserMessage{Content: "new message", Timestamp: after},
	); err != nil {
		t.Fatal(err)
	}

	since := before.Add(time.Minute) // between old and new
	text, err := svc.buildReviewContext(ctx, sess, since)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "new message") {
		t.Errorf("expected fresh message in output, got %q", text)
	}
	if !strings.Contains(text, "<prior_context>") {
		t.Errorf("expected prior_context section, got %q", text)
	}
}

func TestBuildReviewContext_FallbackZeroTimestamps(t *testing.T) {
	// When messages have zero timestamps, they should be treated as fresh.
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	// Append messages WITHOUT timestamps.
	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: "no-timestamp msg"},
	); err != nil {
		t.Fatal(err)
	}

	since := time.Now().UTC().Add(-time.Hour)
	text, err := svc.buildReviewContext(ctx, sess, since)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Error("expected non-empty text when messages lack timestamps")
	}
	if !strings.Contains(text, "no-timestamp msg") {
		t.Errorf("expected message in output, got %q", text)
	}
}

func TestBuildReviewContext_EmptySession(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "empty", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	text, err := svc.buildReviewContext(ctx, sess, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("expected empty text for empty session, got %q", text)
	}
}

func TestBuildReviewContext_FallbackFiltersToolResults(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: "request", Timestamp: now},
		ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "tc1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
			},
			StopReason: ai.StopReasonToolUse,
			Timestamp:  now.Add(time.Second),
		},
		ai.ToolResultMessage{
			ToolCallID: "tc1",
			Content:    []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("x", 10000)}},
			Timestamp:  now.Add(2 * time.Second),
		},
		ai.UserMessage{Content: "thanks", Timestamp: now.Add(3 * time.Second)},
	); err != nil {
		t.Fatal(err)
	}

	text, err := svc.buildReviewContext(ctx, sess, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "[tool]") {
		t.Error("tool result should be filtered out in fallback path")
	}
	if !strings.Contains(text, "request") || !strings.Contains(text, "thanks") {
		t.Errorf("expected user messages preserved, got %q", text)
	}
}

func TestBuildReviewUnit_ChronologicalWindow(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	old := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	freshOne := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	freshTwo := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: "old context", Timestamp: old},
		ai.UserMessage{Content: "fresh one", Timestamp: freshOne},
		ai.UserMessage{Content: strings.Repeat("b", 80), Timestamp: freshTwo},
	); err != nil {
		t.Fatal(err)
	}

	unit, err := svc.buildReviewUnit(ctx, reviewTarget{
		session:         sess,
		privateOneToOne: true,
	}, old.Add(time.Minute), 24)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(unit.Text, "old context") {
		t.Fatalf("expected prior context in review unit, got %q", unit.Text)
	}
	if !strings.Contains(unit.Text, "fresh one") {
		t.Fatalf("expected first fresh message in review unit, got %q", unit.Text)
	}
	if strings.Contains(unit.Text, strings.Repeat("b", 80)) {
		t.Fatalf("did not expect second fresh message beyond budget, got %q", unit.Text)
	}
	if !unit.LastIncludedAt.Equal(freshOne) {
		t.Fatalf("expected last included timestamp %v, got %v", freshOne, unit.LastIncludedAt)
	}
	if unit.FreshCount != 1 {
		t.Fatalf("expected one fresh message included, got %d", unit.FreshCount)
	}
}

func TestBuildReviewUnit_SkipsOversizedSingleMessage(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	hugeAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	smallAt := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	huge := strings.Repeat("x", 2000)
	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: huge, Timestamp: hugeAt},
		ai.UserMessage{Content: "small after huge", Timestamp: smallAt},
	); err != nil {
		t.Fatal(err)
	}

	unit, err := svc.buildReviewUnit(ctx, reviewTarget{
		session:         sess,
		privateOneToOne: true,
	}, time.Time{}, 32)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(unit.Text, huge) {
		t.Fatalf("oversized message should not be included")
	}
	if !strings.Contains(unit.Text, "small after huge") {
		t.Fatalf("expected later small message to be included, got %q", unit.Text)
	}
	if !unit.LastIncludedAt.Equal(smallAt) {
		t.Fatalf("expected watermark to advance to small message %v, got %v", smallAt, unit.LastIncludedAt)
	}
	if len(unit.Skipped) != 1 || unit.Skipped[0].Reason != reviewSkipOversizedSingleMessage {
		t.Fatalf("expected oversized skip, got %#v", unit.Skipped)
	}
}

func TestBuildReviewUnit_IncludesRedactedToolSummary(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	if err := fake.Append(ctx, sess,
		ai.UserMessage{Content: "inspect token", Timestamp: now},
		ai.ToolResultMessage{
			ToolCallID: "tc1",
			ToolName:   "shell",
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "token ghp_abcdefghijklmnopqrstuvwxyz1234567890 plus " + strings.Repeat("z", 3000)},
			},
			Timestamp: now.Add(time.Second),
		},
	); err != nil {
		t.Fatal(err)
	}

	unit, err := svc.buildReviewUnit(ctx, reviewTarget{
		session:         sess,
		privateOneToOne: true,
	}, time.Time{}, 400)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(unit.Text, "[tool_result_summary]") {
		t.Fatalf("expected tool summary in review unit, got %q", unit.Text)
	}
	if strings.Contains(unit.Text, "ghp_abcdefghijklmnopqrstuvwxyz1234567890") {
		t.Fatalf("expected token-like value to be redacted, got %q", unit.Text)
	}
	if strings.Contains(unit.Text, strings.Repeat("z", 3000)) {
		t.Fatalf("expected tool output to be truncated")
	}
}

func TestBuildReviewUnit_FailsClosedForGroupSession(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: &nonReviewerProvider{fake}, log: testLogger()}

	ctx := context.Background()
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "1", GroupID: "g1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: "group memory", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	unit, err := svc.buildReviewUnit(ctx, reviewTarget{
		session:         sess,
		privateOneToOne: false,
	}, time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}

	if unit.Text != "" {
		t.Fatalf("expected group session to be skipped, got %q", unit.Text)
	}
	if len(unit.Skipped) != 1 || unit.Skipped[0].Reason != reviewSkipNotPrivateOneToOne {
		t.Fatalf("expected one-to-one skip, got %#v", unit.Skipped)
	}
}

func TestTailByBudget_FitsAll(t *testing.T) {
	lines := []string{"short", "lines", "here"}
	got := tailByBudget(lines, 10000)
	if len(got) != 3 {
		t.Errorf("expected all 3 lines, got %d", len(got))
	}
}

func TestTailByBudget_Truncates(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens
	lines := []string{big, big, big, "last"}
	got := tailByBudget(lines, 1500)
	// Budget 1500 tokens: "last" (~1 token) + one big (~1000) fits, two bigs don't.
	if len(got) > 2 {
		t.Errorf("expected at most 2 lines within budget, got %d", len(got))
	}
	if len(got) == 0 || got[len(got)-1] != "last" {
		t.Error("expected tail to include the last element")
	}
}

func TestTailByBudget_Empty(t *testing.T) {
	got := tailByBudget(nil, 10000)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d", len(got))
	}
}

// nonReviewerProvider wraps a Fake but hides the Reviewer interface.
// This forces buildReviewContext to use the SessionManager fallback path.
type nonReviewerProvider struct {
	inner *memorytest.Fake
}

func (p *nonReviewerProvider) Name() string { return "non-reviewer" }
func (p *nonReviewerProvider) Bootstrap(ctx context.Context, session memory.Session) error {
	return p.inner.Bootstrap(ctx, session)
}

func (p *nonReviewerProvider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	return p.inner.Append(ctx, session, msgs...)
}

func (p *nonReviewerProvider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	return p.inner.Assemble(ctx, session, budget, freshTail)
}

func (p *nonReviewerProvider) Stats(ctx context.Context, session memory.Session) (memory.SessionStats, error) {
	return p.inner.Stats(ctx, session)
}
func (p *nonReviewerProvider) Close() error { return nil }

// Expose SessionManager but NOT Reviewer.
func (p *nonReviewerProvider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	return p.inner.SaveInfo(ctx, info)
}

func (p *nonReviewerProvider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	return p.inner.LoadInfo(ctx, sessionID)
}

func (p *nonReviewerProvider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	return p.inner.ListInfo(ctx, opts)
}

func (p *nonReviewerProvider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	return p.inner.LoadHistory(ctx, sessionID)
}
