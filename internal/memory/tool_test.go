package memory_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

// bareProvider implements only memory.Provider (no optional capabilities).
type bareProvider struct{}

func (b *bareProvider) Name() string { return "bare" }

func (b *bareProvider) Bootstrap(_ context.Context, _ memory.Session) error { return nil }

func (b *bareProvider) Append(_ context.Context, _ memory.Session, _ ...ai.Message) error {
	return nil
}

func (b *bareProvider) Assemble(_ context.Context, _ memory.Session, _, _ int) ([]ai.Message, error) {
	return nil, nil
}

func (b *bareProvider) Stats(_ context.Context, _ memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{MessageCount: 5, TokenCount: 100}, nil
}

func (b *bareProvider) Close() error { return nil }

// Compile-time check: bareProvider implements only Provider, not any capability.
var _ memory.Provider = (*bareProvider)(nil)

type sessionCaptureProvider struct {
	bareProvider
	got memory.Session
}

func (p *sessionCaptureProvider) Stats(_ context.Context, session memory.Session) (memory.SessionStats, error) {
	p.got = session
	return memory.SessionStats{}, nil
}

func TestExecuteStatus_GroupUsesGroupStorageScopeWithoutUserIdentity(t *testing.T) {
	provider := &sessionCaptureProvider{}
	tool := memory.BuildTool(provider, memory.WithActionsOnly("status"))
	ctx := memory.WithSessionID(context.Background(), "group-session")
	ctx = authz.WithGroupID(ctx, "group-1")
	ctx = authz.WithAgentID(ctx, "agent-1")

	if _, err := tool.Execute(ctx, map[string]any{"action": "status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got, want := provider.got.UserID, "group-1"; got != want {
		t.Fatalf("storage owner = %q, want group %q", got, want)
	}
	if got, want := provider.got.GroupID, "group-1"; got != want {
		t.Fatalf("GroupID = %q, want %q", got, want)
	}
	if got := authz.UserIDFromContext(ctx); got != "" {
		t.Fatalf("group tool minted authenticated user %q", got)
	}
}

func TestBuildTool_FullProvider(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	def := tool.Definition()

	if def.Name != "memory" {
		t.Fatalf("expected tool name 'memory', got %q", def.Name)
	}

	// All actions should be present.
	actions := extractActionEnum(t, def.InputSchema)
	expected := []string{"status", "search", "describe", "expand", "soul_get", "soul_update", "profile_get", "profile_update", "profile_history", "profile_rollback", "constraint_list", "constraint_add", "constraint_remove", "search_knowledge"}
	assertActions(t, actions, expected)

	// Schema should include all action-specific parameters.
	props := def.InputSchema["properties"].(map[string]any)
	for _, key := range []string{"pattern", "query", "scope", "limit", "summary_id", "token_cap", "content", "history_scope", "history_limit", "rollback_version", "constraint_text", "constraint_id"} {
		if _, ok := props[key]; !ok {
			t.Errorf("expected property %q in schema", key)
		}
	}

	// Description should mention all actions.
	for _, a := range expected {
		if !containsString(def.Description, a) {
			t.Errorf("description should mention action %q", a)
		}
	}
}

func TestExecute_ConstraintListAddRemove(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := authz.WithUserID(context.Background(), "1")
	ctx = authz.WithAgentID(ctx, "agent1")

	// Initially empty.
	result, err := tool.Execute(ctx, map[string]any{"action": "constraint_list"})
	if err != nil {
		t.Fatalf("constraint_list error: %v", err)
	}
	if result != "No constraints set." {
		t.Errorf("expected empty message, got %q", result)
	}

	// Add a constraint.
	result, err = tool.Execute(ctx, map[string]any{
		"action":          "constraint_add",
		"constraint_text": "Always respond in English",
	})
	if err != nil {
		t.Fatalf("constraint_add error: %v", err)
	}

	var entries []memory.ConstraintEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("unmarshal add result: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 constraint, got %d", len(entries))
	}
	if entries[0].Text != "Always respond in English" {
		t.Errorf("expected text 'Always respond in English', got %q", entries[0].Text)
	}
	addedID := entries[0].ID

	// List returns the constraint.
	result, err = tool.Execute(ctx, map[string]any{"action": "constraint_list"})
	if err != nil {
		t.Fatalf("constraint_list error: %v", err)
	}
	var listed []memory.ConstraintEntry
	if err := json.Unmarshal([]byte(result), &listed); err != nil {
		t.Fatalf("unmarshal list result: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 constraint in list, got %d", len(listed))
	}

	// Remove the constraint.
	result, err = tool.Execute(ctx, map[string]any{
		"action":        "constraint_remove",
		"constraint_id": addedID,
	})
	if err != nil {
		t.Fatalf("constraint_remove error: %v", err)
	}
	var afterRemove []memory.ConstraintEntry
	if err := json.Unmarshal([]byte(result), &afterRemove); err != nil {
		t.Fatalf("unmarshal remove result: %v", err)
	}
	if len(afterRemove) != 0 {
		t.Errorf("expected 0 constraints after remove, got %d", len(afterRemove))
	}
}

func TestExecute_ConstraintAdd_MissingText(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	ctx := authz.WithUserID(context.Background(), "1")
	ctx = authz.WithAgentID(ctx, "agent1")

	_, err := tool.Execute(ctx, map[string]any{"action": "constraint_add"})
	if err == nil {
		t.Fatal("expected error for missing constraint_text")
	}
}

func TestExecute_ConstraintRemove_MissingID(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	ctx := authz.WithUserID(context.Background(), "1")
	ctx = authz.WithAgentID(ctx, "agent1")

	_, err := tool.Execute(ctx, map[string]any{"action": "constraint_remove"})
	if err == nil {
		t.Fatal("expected error for missing constraint_id")
	}
}

func TestBuildTool_BareProvider(t *testing.T) {
	tool := memory.BuildTool(&bareProvider{})
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)
	assertActions(t, actions, []string{"status"})

	// Should NOT have search/explorer/profile parameters.
	props := def.InputSchema["properties"].(map[string]any)
	for _, key := range []string{"pattern", "scope", "limit", "summary_id", "token_cap", "content"} {
		if _, ok := props[key]; ok {
			t.Errorf("bare provider should not have property %q in schema", key)
		}
	}
}

// fakeWithMessages adds MessageReader to the Fake so the get_message wiring can
// be exercised. The Fake itself is session-scoped and does not model
// cross-session reads, so this keeps that concern out of the shared double.
type fakeWithMessages struct {
	*memorytest.Fake
}

func (fakeWithMessages) GetMessage(_ context.Context, messageID string) (*memory.MessageDetail, error) {
	return &memory.MessageDetail{
		MessageID: messageID,
		Role:      "user",
		Content:   "the complete message body",
		SessionID: "sess-origin",
	}, nil
}

func TestBuildTool_MessageReader(t *testing.T) {
	tool := memory.BuildTool(fakeWithMessages{memorytest.New()})
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)
	if !slices.Contains(actions, "get_message") {
		t.Fatalf("expected get_message action, got %v", actions)
	}
	props := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["message_id"]; !ok {
		t.Error("expected message_id property in schema")
	}

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	out, err := tool.Execute(ctx, map[string]any{"action": "get_message", "message_id": "msg_7"})
	if err != nil {
		t.Fatalf("get_message execute: %v", err)
	}
	var got memory.MessageDetail
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal get_message result: %v", err)
	}
	if got.MessageID != "msg_7" || got.Content != "the complete message body" {
		t.Errorf("unexpected get_message result: %+v", got)
	}

	// A provider without MessageReader must not offer the action.
	bareActions := extractActionEnum(t, memory.BuildTool(memorytest.New()).Definition().InputSchema)
	if slices.Contains(bareActions, "get_message") {
		t.Error("provider without MessageReader should not offer get_message")
	}
}

func TestBuildTool_WithReadOnlyProfile(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake, memory.WithReadOnlyProfile())
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)

	if !containsString2(actions, "profile_get") {
		t.Error("expected profile_get action with WithReadOnlyProfile")
	}
	if containsString2(actions, "profile_update") {
		t.Error("profile_update should be absent with WithReadOnlyProfile")
	}
	// soul_update should still be present.
	if !containsString2(actions, "soul_update") {
		t.Error("expected soul_update action with WithReadOnlyProfile (only profile is read-only)")
	}

	// "content" property should still be present (used by soul_update).
	props := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["content"]; !ok {
		t.Error("content property should be present since soul_update is still available")
	}
}

func TestBuildTool_WithReadOnlySoul(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake, memory.WithReadOnlySoul())
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)

	if !containsString2(actions, "soul_get") {
		t.Error("expected soul_get action with WithReadOnlySoul")
	}
	if containsString2(actions, "soul_update") {
		t.Error("soul_update should be absent with WithReadOnlySoul")
	}
	// profile_update should still be present.
	if !containsString2(actions, "profile_update") {
		t.Error("expected profile_update action with WithReadOnlySoul (only soul is read-only)")
	}
}

func TestBuildTool_WithActionsOnly(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake, memory.WithActionsOnly("status", "search"))
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)
	assertActions(t, actions, []string{"status", "search"})

	// Explorer/profile properties should be absent.
	props := def.InputSchema["properties"].(map[string]any)
	for _, key := range []string{"summary_id", "token_cap", "content"} {
		if _, ok := props[key]; ok {
			t.Errorf("expected property %q to be absent with WithActionsOnly(status, search)", key)
		}
	}
}

func TestBuildTool_WithSessionReadOnlyWrites(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake, memory.WithSessionReadOnlyWrites())
	def := tool.Definition()

	actions := extractActionEnum(t, def.InputSchema)
	assertActions(t, actions, []string{
		"status",
		"search",
		"describe",
		"expand",
		"soul_get",
		"profile_get",
		"profile_history",
		"constraint_list",
		"search_knowledge",
	})

	for _, forbidden := range []string{
		"soul_update",
		"profile_update",
		"profile_rollback",
		"constraint_add",
		"constraint_remove",
	} {
		if containsString2(actions, forbidden) {
			t.Fatalf("session read-only tool exposed write action %q", forbidden)
		}
	}

	props := def.InputSchema["properties"].(map[string]any)
	for _, key := range []string{"content", "rollback_version", "constraint_text", "constraint_id"} {
		if _, ok := props[key]; ok {
			t.Fatalf("session read-only tool exposed write-only schema property %q", key)
		}
	}
}

func TestExecute_Status(t *testing.T) {
	tool := memory.BuildTool(&bareProvider{})
	ctx := memory.WithSessionID(context.Background(), "test-session")

	result, err := tool.Execute(ctx, map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stats memory.SessionStats
	if err := json.Unmarshal([]byte(result), &stats); err != nil {
		t.Fatalf("failed to unmarshal status result: %v", err)
	}
	if stats.MessageCount != 5 {
		t.Errorf("expected MessageCount=5, got %d", stats.MessageCount)
	}
	if stats.TokenCount != 100 {
		t.Errorf("expected TokenCount=100, got %d", stats.TokenCount)
	}
}

func TestExecute_Search(t *testing.T) {
	fake := memorytest.New()
	sess := memory.Session{ID: "s1", AgentID: "agent1", UserID: "1"}
	ctx := context.Background()
	_ = fake.Bootstrap(ctx, sess)
	_ = fake.Append(ctx, sess, ai.UserMessage{
		Content:   "hello world",
		Timestamp: time.Now(),
	})

	tool := memory.BuildTool(fake)
	execCtx := memory.WithSessionID(context.Background(), "s1")
	execCtx = authz.WithAgentID(execCtx, "agent1")
	execCtx = authz.WithUserID(execCtx, "1")

	result, err := tool.Execute(execCtx, map[string]any{
		"action":  "search",
		"pattern": "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []memory.SearchResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("failed to unmarshal search results: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].SourceType != "message" {
		t.Errorf("expected source type 'message', got %q", results[0].SourceType)
	}
}

func TestExecute_SearchNoResults(t *testing.T) {
	fake := memorytest.New()
	sess := memory.Session{ID: "s1"}
	ctx := context.Background()
	_ = fake.Bootstrap(ctx, sess)

	tool := memory.BuildTool(fake)
	execCtx := memory.WithSessionID(context.Background(), "s1")

	result, err := tool.Execute(execCtx, map[string]any{
		"action":  "search",
		"pattern": "nonexistent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("expected 'No matches found.', got %q", result)
	}
}

func TestExecute_SearchKnowledgeCurrentFacts(t *testing.T) {
	fake := memorytest.New()
	now := time.Now().UTC()
	fake.AddFact("1", "agent1", memory.Fact{
		ID:        "world-1",
		Subject:   memory.FactSubjectWorld,
		Content:   "PostgreSQL runtime bundles target Ubuntu LTS.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceManual,
		UpdatedAt: now,
	})
	fake.AddFact("1", "agent1", memory.Fact{
		ID:      "profile-1",
		Subject: memory.FactSubjectUser,
		Content: "The user studies Ubuntu runtime behavior.",
		Status:  memory.FactStatusActive,
	})
	fake.AddFact("1", "agent1", memory.Fact{
		ID:      "agent-1",
		Subject: memory.FactSubjectAgent,
		Content: "The agent knows Ubuntu runtime behavior.",
		Status:  memory.FactStatusActive,
	})

	tool := memory.BuildTool(fake)
	execCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	out, err := tool.Execute(execCtx, map[string]any{
		"action": "search_knowledge",
		"query":  "Ubuntu runtime",
	})
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}

	var results []struct {
		FactID       string  `json:"fact_id"`
		Content      string  `json:"content"`
		MatchedField string  `json:"matched_field"`
		Score        float64 `json:"score"`
		Snippet      string  `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("unmarshal search_knowledge results: %v\n%s", err, out)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want only world fact", results)
	}
	if results[0].FactID != "world-1" || results[0].MatchedField != "content" || results[0].Score <= 0 || results[0].Snippet == "" {
		t.Fatalf("unexpected search_knowledge result: %#v", results[0])
	}
}

type usageTrackingKnowledgeProvider struct {
	*memorytest.Fake
	touchedFactIDs []string
}

func (p *usageTrackingKnowledgeProvider) TouchKnowledgeUsage(_ context.Context, _ string, _ string, factIDs []string) error {
	p.touchedFactIDs = append(p.touchedFactIDs, factIDs...)
	return nil
}

type blockingUsageKnowledgeProvider struct {
	*memorytest.Fake
	deadline time.Time
}

func (p *blockingUsageKnowledgeProvider) TouchKnowledgeUsage(ctx context.Context, _ string, _ string, _ []string) error {
	p.deadline, _ = ctx.Deadline()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1200 * time.Millisecond):
		return nil
	}
}

func TestExecute_SearchKnowledgeTouchesReturnedReflectFacts(t *testing.T) {
	provider := &usageTrackingKnowledgeProvider{Fake: memorytest.New()}
	now := time.Now().UTC()
	provider.AddFact("1", "agent1", memory.Fact{
		ID:        "reflect-world-1",
		Subject:   memory.FactSubjectWorld,
		Content:   "The deployment cluster uses canary rollouts.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceReflect,
		UpdatedAt: now,
	})
	provider.AddFact("1", "agent1", memory.Fact{
		ID:        "manual-world-1",
		Subject:   memory.FactSubjectWorld,
		Content:   "The deployment cluster stores audit logs.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceManual,
		UpdatedAt: now,
	})

	tool := memory.BuildTool(provider)
	execCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	out, err := tool.Execute(execCtx, map[string]any{
		"action": "search_knowledge",
		"query":  "canary rollouts",
	})
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	if !strings.Contains(out, "reflect-world-1") {
		t.Fatalf("expected reflect fact result: %s", out)
	}
	if !slices.Equal(provider.touchedFactIDs, []string{"reflect-world-1"}) {
		t.Fatalf("touchedFactIDs = %#v, want only returned reflect fact", provider.touchedFactIDs)
	}
}

func TestExecute_SearchKnowledgeBoundsBestEffortUsageLatency(t *testing.T) {
	provider := &blockingUsageKnowledgeProvider{Fake: memorytest.New()}
	provider.AddFact("1", "agent1", memory.Fact{
		ID: "reflect-world-timeout", Subject: memory.FactSubjectWorld,
		Content: "The deployment uses bounded usage tracking.", Status: memory.FactStatusActive,
		Source: memory.SourceReflect, UpdatedAt: time.Now().UTC(),
	})
	tool := memory.BuildTool(provider)
	execCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	started := time.Now()
	out, err := tool.Execute(execCtx, map[string]any{"action": "search_knowledge", "query": "bounded usage tracking"})
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	if !strings.Contains(out, "reflect-world-timeout") {
		t.Fatalf("main search result lost after usage timeout: %s", out)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("search_knowledge blocked for %s, want bounded best-effort touch", elapsed)
	}
	if provider.deadline.IsZero() {
		t.Fatal("usage tracker did not receive a deadline")
	}
	if remaining := provider.deadline.Sub(started); remaining > 600*time.Millisecond {
		t.Fatalf("usage deadline = %s after start, want about 500ms", remaining)
	}
}

type snapshotKnowledgeProvider struct {
	*memorytest.Fake
	atVersion       int64
	versionedCall   bool
	currentCalls    int
	snapshotVersion int64
}

func (p *snapshotKnowledgeProvider) GetOrCreateSessionSnapshot(_ context.Context, sessionID string, userID string, agentID string) (memory.SessionSnapshot, error) {
	return memory.SessionSnapshot{
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
		Version:   p.snapshotVersion,
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func (p *snapshotKnowledgeProvider) ListActiveFactsAt(_ context.Context, userID string, agentID string, subject memory.FactSubject, version int64) ([]memory.Fact, error) {
	p.atVersion = version
	p.versionedCall = true
	return []memory.Fact{
		{
			ID:      "snapshot-world",
			Subject: subject,
			Scope:   "user_agent",
			UserID:  userID,
			AgentID: agentID,
			Content: "Snapshot-visible deployment region is us-west.",
			Status:  memory.FactStatusActive,
			Source:  memory.SourceManual,
		},
	}, nil
}

func (p *snapshotKnowledgeProvider) ListActiveFacts(ctx context.Context, userID string, agentID string, subject memory.FactSubject) ([]memory.Fact, error) {
	p.currentCalls++
	return p.Fake.ListActiveFacts(ctx, userID, agentID, subject)
}

func TestSearchKnowledgeUsesFrozenVersionZero(t *testing.T) {
	provider := &snapshotKnowledgeProvider{Fake: memorytest.New()}
	tool := memory.BuildTool(provider)
	execCtx := memory.WithSessionID(context.Background(), "s1")
	execCtx = authz.WithUserID(execCtx, "1")
	execCtx = authz.WithAgentID(execCtx, "agent1")

	_, err := tool.Execute(execCtx, map[string]any{
		"action": "search_knowledge",
		"query":  "deployment region",
	})
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	if !provider.versionedCall || provider.atVersion != 0 {
		t.Fatalf("ListActiveFactsAt called at version %d, want 0", provider.atVersion)
	}
	if provider.currentCalls != 0 {
		t.Fatalf("current fact reads = %d, want 0", provider.currentCalls)
	}
}

func TestExecute_SearchKnowledgeUsesSessionSnapshot(t *testing.T) {
	provider := &snapshotKnowledgeProvider{Fake: memorytest.New(), snapshotVersion: 7}
	tool := memory.BuildTool(provider)
	execCtx := memory.WithSessionID(context.Background(), "s1")
	execCtx = authz.WithUserID(execCtx, "1")
	execCtx = authz.WithAgentID(execCtx, "agent1")

	out, err := tool.Execute(execCtx, map[string]any{
		"action": "search_knowledge",
		"query":  "deployment region",
	})
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	if provider.atVersion != 7 {
		t.Fatalf("ListActiveFactsAt version = %d, want 7", provider.atVersion)
	}
	if !strings.Contains(out, "snapshot-world") {
		t.Fatalf("expected snapshot-visible fact in results: %s", out)
	}
}

func TestExecute_SearchKnowledgeNoUserContextFailsClosed(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	ctx := authz.WithAgentID(context.Background(), "agent1")
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{UserID: "speaker-user"})

	_, err := tool.Execute(ctx, map[string]any{
		"action": "search_knowledge",
		"query":  "anything",
	})
	if err == nil {
		t.Fatal("expected search_knowledge to fail without session user context")
	}
	if !strings.Contains(err.Error(), "no user context") {
		t.Fatalf("error = %q, want no user context", err.Error())
	}
}

func TestExecute_Describe(t *testing.T) {
	fake := memorytest.New()
	now := time.Now()
	fake.AddSummary(memorytest.FakeSummary{
		ID:         "sum_abc",
		Kind:       "leaf",
		Depth:      0,
		Content:    "Test summary",
		EarliestAt: &now,
		LatestAt:   &now,
	})

	tool := memory.BuildTool(fake)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":     "describe",
		"summary_id": "sum_abc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var desc memory.DescribeResult
	if err := json.Unmarshal([]byte(result), &desc); err != nil {
		t.Fatalf("failed to unmarshal describe result: %v", err)
	}
	if desc.SummaryID != "sum_abc" {
		t.Errorf("expected summary ID 'sum_abc', got %q", desc.SummaryID)
	}
}

func TestExecute_Expand(t *testing.T) {
	fake := memorytest.New()
	fake.AddSummary(memorytest.FakeSummary{
		ID:   "sum_xyz",
		Kind: "leaf",
		SourceMessages: []memory.ExpandMessage{
			{MessageID: "1", Role: "user", Content: "msg1"},
		},
	})

	tool := memory.BuildTool(fake)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":     "expand",
		"summary_id": "sum_xyz",
		"token_cap":  float64(2000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var exp memory.ExpandResult
	if err := json.Unmarshal([]byte(result), &exp); err != nil {
		t.Fatalf("failed to unmarshal expand result: %v", err)
	}
	if len(exp.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(exp.Messages))
	}
}

func TestExecute_ProfileGetAndUpdate(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := authz.WithUserID(context.Background(), "42")
	ctx = authz.WithAgentID(ctx, "agent1")

	// profile_get on empty profile.
	result, err := tool.Execute(ctx, map[string]any{"action": "profile_get"})
	if err != nil {
		t.Fatalf("unexpected error on profile_get: %v", err)
	}
	if result != "No profile notes found." {
		t.Errorf("expected empty profile message, got %q", result)
	}

	// profile_update.
	result, err = tool.Execute(ctx, map[string]any{
		"action":  "profile_update",
		"content": "Likes Go and tea",
	})
	if err != nil {
		t.Fatalf("unexpected error on profile_update: %v", err)
	}
	if !containsString(result, "Profile updated") {
		t.Errorf("expected update confirmation, got %q", result)
	}

	// profile_get should now return the content.
	result, err = tool.Execute(ctx, map[string]any{"action": "profile_get"})
	if err != nil {
		t.Fatalf("unexpected error on profile_get: %v", err)
	}
	if result != "Likes Go and tea" {
		t.Errorf("expected 'Likes Go and tea', got %q", result)
	}
}

func TestExecute_SoulGetAndUpdate(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := authz.WithUserID(context.Background(), "42")
	ctx = authz.WithAgentID(ctx, "agent1")

	// soul_get on empty soul.
	result, err := tool.Execute(ctx, map[string]any{"action": "soul_get"})
	if err != nil {
		t.Fatalf("unexpected error on soul_get: %v", err)
	}
	if result != "No agent soul defined." {
		t.Errorf("expected empty soul message, got %q", result)
	}

	// soul_update.
	result, err = tool.Execute(ctx, map[string]any{
		"action":  "soul_update",
		"content": "Be concise and friendly.",
	})
	if err != nil {
		t.Fatalf("unexpected error on soul_update: %v", err)
	}
	if !containsString(result, "Agent soul updated") {
		t.Errorf("expected update confirmation, got %q", result)
	}

	// soul_get should now return the content.
	result, err = tool.Execute(ctx, map[string]any{"action": "soul_get"})
	if err != nil {
		t.Fatalf("unexpected error on soul_get: %v", err)
	}
	if result != "Be concise and friendly." {
		t.Errorf("expected soul content, got %q", result)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	tool := memory.BuildTool(&bareProvider{})
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !containsString(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' in error, got %q", err.Error())
	}
}

func TestExecute_SearchMissingPattern(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "search"})
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
}

func TestExecute_ProfileUpdateMissingContent(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	ctx := authz.WithUserID(context.Background(), "1")
	ctx = authz.WithAgentID(ctx, "a")
	_, err := tool.Execute(ctx, map[string]any{"action": "profile_update"})
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestExecute_ProfileNoUserContext(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "profile_get"})
	if err == nil {
		t.Fatal("expected error for missing user context")
	}
}

func TestExecute_DescribeMissingSummaryID(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "describe"})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
}

func TestExecute_ExpandMissingSummaryID(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "expand"})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func extractActionEnum(t *testing.T, schema map[string]any) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	actionProp, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("schema missing action property")
	}
	enumRaw, ok := actionProp["enum"].([]any)
	if !ok {
		t.Fatal("action property missing enum")
	}
	var actions []string
	for _, v := range enumRaw {
		actions = append(actions, v.(string))
	}
	return actions
}

func assertActions(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected actions %v, got %v", want, got)
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("unexpected action %q", g)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsString2(slice []string, s string) bool {
	return slices.Contains(slice, s)
}
