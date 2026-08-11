package memory_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestBuildTool_WithoutTranscriptActions(t *testing.T) {
	provider := fakeWithMessages{memorytest.New()}
	tool := memory.BuildTool(provider, memory.WithSessionReadOnlyWrites(), memory.WithoutTranscriptActions())
	def := tool.Definition()
	actions := extractActionEnum(t, def.InputSchema)

	for _, removed := range []string{"status", "search", "describe", "expand", "get_message"} {
		if slices.Contains(actions, removed) {
			t.Fatalf("model-facing memory tool exposed transcript action %q: %v", removed, actions)
		}
	}
	for _, kept := range []string{"soul_get", "profile_get", "profile_history", "constraint_list", "search_knowledge"} {
		if !slices.Contains(actions, kept) {
			t.Fatalf("model-facing memory tool omitted non-transcript action %q: %v", kept, actions)
		}
	}
	if _, err := tool.Execute(t.Context(), map[string]any{"action": "search", "pattern": "old chat"}); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("removed search action error = %v", err)
	}
}

type fakeRecallSource struct {
	hits               []memory.RecallSearchResult
	docs               map[string]memory.RecallDocument
	requestedSearchCap int
	requestedReadCap   int
}

func (f *fakeRecallSource) SearchRecall(_ context.Context, _ authz.Authority, _, _ string, limit int) ([]memory.RecallSearchResult, error) {
	f.requestedSearchCap = limit
	return f.hits, nil
}

func (f *fakeRecallSource) ReadRecall(_ context.Context, _ authz.Authority, _ string, ref memory.RecallReference, tokenCap int) (memory.RecallDocument, error) {
	f.requestedReadCap = tokenCap
	doc, ok := f.docs[ref.Kind+":"+ref.ID+":"+ref.SessionID]
	if !ok {
		return memory.RecallDocument{}, fmt.Errorf("not found")
	}
	return doc, nil
}

func TestBuildTool_WithRecallSourceExposesOnlyUnifiedActions(t *testing.T) {
	tool := memory.BuildTool(memorytest.New(), memory.WithRecallSource(&fakeRecallSource{}))
	actions := extractActionEnum(t, tool.Definition().InputSchema)
	assertActions(t, actions, []string{"search", "read"})

	properties := tool.Definition().InputSchema["properties"].(map[string]any)
	for _, required := range []string{"query", "limit", "ref", "token_cap"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("unified memory schema omitted %q", required)
		}
	}
	for _, hidden := range []string{"pattern", "scope", "summary_id", "message_id", "history_scope", "constraint_id"} {
		if _, ok := properties[hidden]; ok {
			t.Fatalf("unified memory schema exposed internal selector %q", hidden)
		}
	}
}

func TestUnifiedMemorySearchAndReadFederatesRecallAndDurableMemory(t *testing.T) {
	fake := memorytest.New()
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	if err := fake.SetProfile(ctx, "user-1", "agent-1", "Prefers jasmine tea"); err != nil {
		t.Fatal(err)
	}
	if err := fake.SetAgentSoul(ctx, "user-1", "agent-1", "Be concise and calm"); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.AddConstraint(ctx, "user-1", "agent-1", "Ask before deleting files"); err != nil {
		t.Fatal(err)
	}
	version := int64(1)
	for _, scope := range []string{"profile", "soul"} {
		if err := fake.WriteChangelog(ctx, memory.ChangeEntry{
			ID: scope + "-version", UserID: "user-1", AgentID: "agent-1", Scope: scope,
			Action: "update", Source: memory.SourceManual, MemoryVersionAfter: &version,
			AfterText: scope + " version one", CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fake.AddFact("user-1", "agent-1", memory.Fact{
		ID: "fact-1", Subject: memory.FactSubjectWorld, Scope: "user_agent", UserID: "user-1", AgentID: "agent-1",
		Content: "Tea meeting is on Friday", Status: memory.FactStatusActive, Source: memory.SourceReflect, UpdatedAt: time.Now().UTC(),
	})
	now := time.Now().UTC()
	messageRef := memory.RecallReference{Kind: "message", ID: "message-1", SessionID: "session-1"}
	source := &fakeRecallSource{
		hits: []memory.RecallSearchResult{{
			Reference: messageRef, Content: "We discussed tea yesterday", OccurredAt: now,
			SessionID: "session-1", ConversationTitle: "Tea plans",
		}},
		docs: map[string]memory.RecallDocument{
			"message:message-1:session-1": {
				Reference: messageRef, Content: "We discussed tea yesterday in full", Role: "user", OccurredAt: now,
				SessionID: "session-1", ConversationTitle: "Tea plans",
			},
		},
	}
	tool := memory.BuildTool(fake, memory.WithRecallSource(source))

	out, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "tea"})
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Results []struct {
			Ref        string `json:"ref"`
			Snippet    string `json:"snippet"`
			Provenance *struct {
				SessionID string `json:"session_id"`
			} `json:"provenance"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 3 {
		t.Fatalf("unified results=%#v, want transcript, profile, and fact", search.Results)
	}
	if strings.Contains(out, "source_type") || strings.Contains(out, "source_id") || strings.Contains(out, "fact_id") {
		t.Fatalf("unified search leaked storage selectors: %s", out)
	}
	var transcriptRef, factRef string
	for _, result := range search.Results {
		if result.Provenance != nil && result.Provenance.SessionID == "session-1" {
			transcriptRef = result.Ref
		}
		if strings.Contains(result.Snippet, "Friday") {
			factRef = result.Ref
		}
	}
	if transcriptRef == "" || factRef == "" {
		t.Fatalf("search did not return readable refs: %s", out)
	}

	read, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": transcriptRef})
	if err != nil || !strings.Contains(read, "in full") || !strings.Contains(read, "session-1") {
		t.Fatalf("read transcript ref: output=%s err=%v", read, err)
	}
	read, err = tool.Execute(ctx, map[string]any{"action": "read", "ref": factRef})
	if err != nil || !strings.Contains(read, "Friday") {
		t.Fatalf("read fact ref: output=%s err=%v", read, err)
	}
	foreignCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-2"), "agent-1")
	if _, err := tool.Execute(foreignCtx, map[string]any{"action": "read", "ref": factRef}); err == nil {
		t.Fatal("foreign user read a forged durable-memory ref")
	}
	read, err = tool.Execute(ctx, map[string]any{"action": "read", "ref": "profile"})
	if err != nil || !strings.Contains(read, "jasmine tea") {
		t.Fatalf("read well-known profile: output=%s err=%v", read, err)
	}
	for _, tc := range []struct {
		ref  string
		want string
	}{
		{ref: "soul", want: "concise and calm"},
		{ref: "constraints", want: "Ask before deleting files"},
		{ref: "profile_versions", want: "profile version one"},
		{ref: "soul_versions", want: "soul version one"},
	} {
		read, err = tool.Execute(ctx, map[string]any{"action": "read", "ref": tc.ref})
		if err != nil || !strings.Contains(read, tc.want) {
			t.Fatalf("read well-known %s: output=%s err=%v", tc.ref, read, err)
		}
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": "mem1.not-valid"}); err == nil {
		t.Fatal("malformed memory ref was accepted")
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": strings.Repeat("x", 4_097)}); err == nil {
		t.Fatal("oversized memory ref was accepted")
	}
}

func TestUnifiedMemorySearchAndReadAreBounded(t *testing.T) {
	const sessionID = "session-1"
	oversizedTitle := strings.Repeat("界", 50_000)
	hits := make([]memory.RecallSearchResult, 60)
	docs := make(map[string]memory.RecallDocument, len(hits))
	for i := range hits {
		ref := memory.RecallReference{Kind: "message", ID: fmt.Sprintf("message-%d", i), SessionID: sessionID}
		hits[i] = memory.RecallSearchResult{Reference: ref, Content: strings.Repeat("x", 2_000), SessionID: sessionID, ConversationTitle: oversizedTitle}
		docs[fmt.Sprintf("message:message-%d:%s", i, sessionID)] = memory.RecallDocument{
			Reference: ref, Content: strings.Repeat("y", 100_000), SessionID: sessionID, ConversationTitle: oversizedTitle,
		}
	}
	source := &fakeRecallSource{hits: hits, docs: docs}
	tool := memory.BuildTool(memorytest.New(), memory.WithRecallSource(source))
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")

	out, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "x", "limit": 1_000})
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Results []struct {
			Ref     string `json:"ref"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil {
		t.Fatal(err)
	}
	if source.requestedSearchCap != 100 || len(search.Results) != 50 {
		t.Fatalf("search bounds: source cap=%d results=%d", source.requestedSearchCap, len(search.Results))
	}
	for _, result := range search.Results {
		if len(result.Snippet) > 1_000 {
			t.Fatalf("search snippet bytes=%d, want <=1000", len(result.Snippet))
		}
	}

	read, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": search.Results[0].Ref, "token_cap": 100_000})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Content    string `json:"content"`
		Truncated  bool   `json:"truncated"`
		Provenance struct {
			Title string `json:"title"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal([]byte(read), &response); err != nil {
		t.Fatal(err)
	}
	if source.requestedReadCap != 8_000 || len(response.Content) != 64_000 || !response.Truncated {
		t.Fatalf("read bounds: source cap=%d bytes=%d truncated=%t", source.requestedReadCap, len(response.Content), response.Truncated)
	}
	if len(response.Provenance.Title) > 1_000 || !utf8.ValidString(response.Provenance.Title) {
		t.Fatalf("read provenance title bytes=%d valid_utf8=%t, want <=1000 valid bytes", len(response.Provenance.Title), utf8.ValidString(response.Provenance.Title))
	}
	if len(read) > 128_000 {
		t.Fatalf("serialized memory.read bytes=%d, want <=128000", len(read))
	}
}

func TestUnifiedMemoryReadPreservesCondensedSummaryMetadata(t *testing.T) {
	depth := 0
	ref := memory.RecallReference{Kind: "summary", ID: "root", SessionID: "session-1"}
	children := make([]memory.RecallReference, 1_000)
	expanded := make([]memory.RecallFragment, len(children))
	for i := range children {
		children[i] = memory.RecallReference{Kind: "summary", ID: fmt.Sprintf("child-%d", i), SessionID: "session-1"}
		expanded[i] = memory.RecallFragment{Reference: children[i], Kind: "leaf", Depth: &depth, Content: ""}
	}
	source := &fakeRecallSource{
		hits: []memory.RecallSearchResult{{Reference: ref, Content: "condensed root", SessionID: "session-1"}},
		docs: map[string]memory.RecallDocument{
			"summary:root:session-1": {
				Reference: ref, Content: "condensed root", Authority: "information_only", SessionID: "session-1",
				Summary: &memory.RecallSummaryDetail{
					Kind: "condensed", Depth: 1,
					Children: children,
					Expanded: expanded,
				},
			},
		},
	}
	tool := memory.BuildTool(memorytest.New(), memory.WithRecallSource(source))
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	out, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "condensed"})
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Results []struct {
			Ref string `json:"ref"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil || len(search.Results) != 1 {
		t.Fatalf("summary search: output=%s err=%v", out, err)
	}
	read, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": search.Results[0].Ref})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Authority string `json:"authority"`
		Summary   struct {
			ChildRefs []string `json:"child_refs"`
			Expanded  []struct {
				Kind  string `json:"kind"`
				Depth *int   `json:"depth"`
			} `json:"expanded"`
			Truncated bool `json:"truncated"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(read), &response); err != nil {
		t.Fatal(err)
	}
	if response.Authority != "information_only" {
		t.Fatalf("model-facing summary authority was lost: %s", read)
	}
	if len(response.Summary.Expanded) == 0 || len(response.Summary.Expanded) > 200 || len(response.Summary.ChildRefs) == 0 || len(response.Summary.ChildRefs) > 200 || !response.Summary.Truncated {
		t.Fatalf("model-facing summary arrays were not bounded: %s", read)
	}
	if response.Summary.Expanded[0].Kind != "leaf" || response.Summary.Expanded[0].Depth == nil || *response.Summary.Expanded[0].Depth != 0 {
		t.Fatalf("model-facing condensed metadata was lost: %s", read)
	}
	if len(read) > 128_000 {
		t.Fatalf("serialized summary read bytes=%d, want <=128000", len(read))
	}
}

func TestUnifiedMemoryReadBoundsConstraints(t *testing.T) {
	fake := memorytest.New()
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	for i := range 150 {
		text := fmt.Sprintf("constraint-%d-%s", i, strings.Repeat("x", 5_000))
		if i == 149 {
			text += " unique-tail-needle"
		}
		if _, err := fake.AddConstraint(ctx, "user-1", "agent-1", text); err != nil {
			t.Fatal(err)
		}
	}
	tool := memory.BuildTool(fake, memory.WithRecallSource(&fakeRecallSource{}))
	out, err := tool.Execute(ctx, map[string]any{"action": "read", "ref": "constraints"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Constraints []memory.ConstraintEntry `json:"constraints"`
		Truncated   bool                     `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Constraints) == 0 || len(response.Constraints) > 100 || !response.Truncated || len(out) > 128_000 {
		t.Fatalf("bounded constraints: count=%d truncated=%t bytes=%d", len(response.Constraints), response.Truncated, len(out))
	}
	for _, constraint := range response.Constraints {
		if len(constraint.Text) > 4_000 {
			t.Fatalf("constraint text bytes=%d, want <=4000", len(constraint.Text))
		}
	}
	search, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "unique-tail-needle"})
	if err != nil || !strings.Contains(search, `"ref": "constraints"`) {
		t.Fatalf("memory.search did not search constraints beyond read output window: output=%s err=%v", search, err)
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

func TestUnifiedMemorySearchAndReadUseSessionSnapshot(t *testing.T) {
	provider := &snapshotKnowledgeProvider{Fake: memorytest.New(), snapshotVersion: 7}
	tool := memory.BuildTool(provider, memory.WithRecallSource(&fakeRecallSource{}))
	execCtx := memory.WithSessionID(context.Background(), "s1")
	execCtx = authz.WithUserID(execCtx, "1")
	execCtx = authz.WithAgentID(execCtx, "agent1")

	out, err := tool.Execute(execCtx, map[string]any{
		"action": "search",
		"query":  "deployment region",
	})
	if err != nil {
		t.Fatalf("memory.search: %v", err)
	}
	var search struct {
		Results []struct {
			Ref string `json:"ref"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil {
		t.Fatal(err)
	}
	if provider.atVersion != 7 || provider.currentCalls != 0 || len(search.Results) != 1 {
		t.Fatalf("snapshot search: version=%d current_calls=%d results=%s", provider.atVersion, provider.currentCalls, out)
	}
	read, err := tool.Execute(execCtx, map[string]any{"action": "read", "ref": search.Results[0].Ref})
	if err != nil || !strings.Contains(read, "us-west") {
		t.Fatalf("snapshot memory.read: output=%s err=%v", read, err)
	}
	if provider.atVersion != 7 || provider.currentCalls != 0 {
		t.Fatalf("snapshot read: version=%d current_calls=%d", provider.atVersion, provider.currentCalls)
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
