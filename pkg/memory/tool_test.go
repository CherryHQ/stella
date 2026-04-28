package memory_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/memory/memorytest"
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

func TestBuildTool_FullProvider(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	def := tool.Definition()

	if def.Name != "memory" {
		t.Fatalf("expected tool name 'memory', got %q", def.Name)
	}

	// All actions should be present.
	actions := extractActionEnum(t, def.InputSchema)
	expected := []string{"status", "search", "describe", "expand", "soul_get", "soul_update", "profile_get", "profile_update", "profile_history", "profile_rollback", "constraint_list", "constraint_add", "constraint_remove"}
	assertActions(t, actions, expected)

	// Schema should include all action-specific parameters.
	props := def.InputSchema["properties"].(map[string]any)
	for _, key := range []string{"pattern", "scope", "limit", "summary_id", "token_cap", "content", "history_scope", "history_limit", "rollback_version", "constraint_text", "constraint_id"} {
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

	ctx := memory.WithUserID(context.Background(), 1)
	ctx = memory.WithAgentID(ctx, "agent1")

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
	ctx := memory.WithUserID(context.Background(), 1)
	ctx = memory.WithAgentID(ctx, "agent1")

	_, err := tool.Execute(ctx, map[string]any{"action": "constraint_add"})
	if err == nil {
		t.Fatal("expected error for missing constraint_text")
	}
}

func TestExecute_ConstraintRemove_MissingID(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)
	ctx := memory.WithUserID(context.Background(), 1)
	ctx = memory.WithAgentID(ctx, "agent1")

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
	sess := memory.Session{ID: "s1", AgentID: "agent1", UserID: 1}
	ctx := context.Background()
	_ = fake.Bootstrap(ctx, sess)
	_ = fake.Append(ctx, sess, ai.UserMessage{
		Content:   "hello world",
		Timestamp: time.Now(),
	})

	tool := memory.BuildTool(fake)
	execCtx := memory.WithSessionID(context.Background(), "s1")
	execCtx = memory.WithAgentID(execCtx, "agent1")
	execCtx = memory.WithUserID(execCtx, 1)

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
			{MessageID: 1, Role: "user", Content: "msg1"},
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

	ctx := memory.WithUserID(context.Background(), 42)
	ctx = memory.WithAgentID(ctx, "agent1")

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

	ctx := memory.WithUserID(context.Background(), 42)
	ctx = memory.WithAgentID(ctx, "agent1")

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
	ctx := memory.WithUserID(context.Background(), 1)
	ctx = memory.WithAgentID(ctx, "a")
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
