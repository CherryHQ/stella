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
	"github.com/CherryHQ/stella/pkg/tools"
)

type fakeRecallSource struct {
	hits               []memory.RecallSearchResult
	docs               map[string]memory.RecallDocument
	requestedSearchCap int
	requestedReadCap   int
}

type fakeGroupRecallSource struct {
	searches int
	reads    int
	groupID  string
	seq      int64
	rows     []memory.GroupRecallResult
}

func (f *fakeGroupRecallSource) SearchGroupRecall(_ context.Context, groupID string, triggerSeq int64, _ string, _ int) ([]memory.GroupRecallResult, error) {
	f.searches++
	f.groupID, f.seq = groupID, triggerSeq
	return f.rows, nil
}

func (f *fakeGroupRecallSource) ReadGroupRecall(_ context.Context, groupID string, triggerSeq int64, messageID string, _ int) ([]memory.GroupRecallResult, bool, error) {
	f.reads++
	f.groupID, f.seq = groupID, triggerSeq
	for _, row := range f.rows {
		if row.ID == messageID {
			return f.rows, false, nil
		}
	}
	return nil, false, memory.ErrGroupRecallNotFound
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

// privateTools is splitTools for the ordinary one-to-one turn, where the group
// lane is not wired at all.
func privateTools(t *testing.T, provider memory.Provider, private memory.RecallSource) map[string]tools.Tool {
	t.Helper()
	return splitTools(t, provider, private, nil)
}

func TestMemorySearchAndReadFederateRecallAndDurableMemory(t *testing.T) {
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
	split := privateTools(t, fake, source)

	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "tea"})
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

	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": transcriptRef})
	if err != nil || !strings.Contains(read, "in full") || !strings.Contains(read, "session-1") {
		t.Fatalf("read transcript ref: output=%s err=%v", read, err)
	}
	read, err = split["memory_read"].Execute(ctx, map[string]any{"ref": factRef})
	if err != nil || !strings.Contains(read, "Friday") {
		t.Fatalf("read fact ref: output=%s err=%v", read, err)
	}
	foreignCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-2"), "agent-1")
	if _, err := split["memory_read"].Execute(foreignCtx, map[string]any{"ref": factRef}); err == nil {
		t.Fatal("foreign user read a forged durable-memory ref")
	}
	read, err = split["memory_read"].Execute(ctx, map[string]any{"ref": "profile"})
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
		read, err = split["memory_read"].Execute(ctx, map[string]any{"ref": tc.ref})
		if err != nil || !strings.Contains(read, tc.want) {
			t.Fatalf("read well-known %s: output=%s err=%v", tc.ref, read, err)
		}
	}
	if _, err := split["memory_read"].Execute(ctx, map[string]any{"ref": "mem1.not-valid"}); err == nil {
		t.Fatal("malformed memory ref was accepted")
	}
	if _, err := split["memory_read"].Execute(ctx, map[string]any{"ref": strings.Repeat("x", 4_097)}); err == nil {
		t.Fatal("oversized memory ref was accepted")
	}
}

func TestMemorySearchAndReadAreBounded(t *testing.T) {
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
	split := privateTools(t, memorytest.New(), source)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")

	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "x", "limit": 1_000})
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

	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": search.Results[0].Ref, "token_cap": 100_000})
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
		t.Fatalf("serialized memory_read bytes=%d, want <=128000", len(read))
	}
}

func TestMemoryReadPreservesCondensedSummaryMetadata(t *testing.T) {
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
	split := privateTools(t, memorytest.New(), source)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "condensed"})
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
	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": search.Results[0].Ref})
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

func TestMemoryReadBoundsConstraints(t *testing.T) {
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
	split := privateTools(t, fake, &fakeRecallSource{})
	out, err := split["memory_read"].Execute(ctx, map[string]any{"ref": "constraints"})
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
	search, err := split["memory_search"].Execute(ctx, map[string]any{"q": "unique-tail-needle"})
	if err != nil || !strings.Contains(search, `"ref": "constraints"`) {
		t.Fatalf("memory_search did not search constraints beyond read output window: output=%s err=%v", search, err)
	}
}

// The profile is the one durable store a turn with no session user may resolve
// through the current speaker. Soul, constraints, and conversation recall stay
// on the session user and fail closed (D9).
func TestMemoryReadPreservesOnlyProfileSpeakerFallback(t *testing.T) {
	fake := memorytest.New()
	ctx := memory.WithCurrentSpeaker(authz.WithAgentID(context.Background(), "agent1"), memory.CurrentSpeaker{UserID: "speaker1"})
	if err := fake.SetProfile(ctx, "speaker1", "agent1", "Speaker likes tea"); err != nil {
		t.Fatal(err)
	}
	split := privateTools(t, fake, &fakeRecallSource{})

	result, err := split["memory_read"].Execute(ctx, map[string]any{"ref": "profile"})
	if err != nil || !strings.Contains(result, "Speaker likes tea") {
		t.Fatalf("read profile fallback: result=%q err=%v", result, err)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_read", map[string]any{"ref": "soul"}},
		{"memory_read", map[string]any{"ref": "constraints"}},
		{"memory_search", map[string]any{"q": "tea"}},
	} {
		if _, err := split[tc.tool].Execute(ctx, tc.args); err == nil {
			t.Fatalf("%s unexpectedly widened access: %#v", tc.tool, tc.args)
		}
	}
}

func TestMemorySearchReturnsOnlyWorldKnowledgeFacts(t *testing.T) {
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

	split := privateTools(t, fake, &fakeRecallSource{})
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "Ubuntu runtime"})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}

	var search struct {
		Results []struct {
			Ref     string `json:"ref"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil {
		t.Fatalf("unmarshal memory_search results: %v\n%s", err, out)
	}
	if len(search.Results) != 1 || !strings.Contains(search.Results[0].Snippet, "Ubuntu LTS") {
		t.Fatalf("results = %#v, want only the world fact", search.Results)
	}
	// Subject-scoped facts back the profile and the agent's own identity; they
	// reach the model through those refs, never as free-standing recall hits.
	if strings.Contains(out, "studies Ubuntu") || strings.Contains(out, "agent knows Ubuntu") {
		t.Fatalf("memory_search returned a non-world fact: %s", out)
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

// Reflect retires facts nothing recalls, so a returned reflect fact has to be
// marked as used — and only the ones actually handed back.
func TestMemorySearchTouchesReturnedReflectFacts(t *testing.T) {
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

	split := privateTools(t, provider, &fakeRecallSource{})
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "canary rollouts"})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !strings.Contains(out, "canary rollouts") {
		t.Fatalf("expected reflect fact result: %s", out)
	}
	if !slices.Equal(provider.touchedFactIDs, []string{"reflect-world-1"}) {
		t.Fatalf("touchedFactIDs = %#v, want only the returned reflect fact", provider.touchedFactIDs)
	}
}

// Usage tracking is bookkeeping, not the answer: a slow tracker must not hold
// the recall the model is waiting for.
func TestMemorySearchBoundsBestEffortUsageLatency(t *testing.T) {
	provider := &blockingUsageKnowledgeProvider{Fake: memorytest.New()}
	provider.AddFact("1", "agent1", memory.Fact{
		ID: "reflect-world-timeout", Subject: memory.FactSubjectWorld,
		Content: "The deployment uses bounded usage tracking.", Status: memory.FactStatusActive,
		Source: memory.SourceReflect, UpdatedAt: time.Now().UTC(),
	})
	split := privateTools(t, provider, &fakeRecallSource{})
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "1"), "agent1")
	started := time.Now()
	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "bounded usage tracking"})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !strings.Contains(out, "bounded usage tracking") {
		t.Fatalf("main search result lost after usage timeout: %s", out)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("memory_search blocked for %s, want bounded best-effort touch", elapsed)
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

// Version zero is a real frozen version, not "no snapshot": a session that has
// not advanced yet must still read the frozen view.
func TestMemorySearchUsesFrozenVersionZero(t *testing.T) {
	provider := &snapshotKnowledgeProvider{Fake: memorytest.New()}
	split := privateTools(t, provider, &fakeRecallSource{})
	ctx := memory.WithSessionID(context.Background(), "s1")
	ctx = authz.WithUserID(ctx, "1")
	ctx = authz.WithAgentID(ctx, "agent1")

	if _, err := split["memory_search"].Execute(ctx, map[string]any{"q": "deployment region"}); err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !provider.versionedCall || provider.atVersion != 0 {
		t.Fatalf("ListActiveFactsAt called at version %d, want 0", provider.atVersion)
	}
	if provider.currentCalls != 0 {
		t.Fatalf("current fact reads = %d, want 0", provider.currentCalls)
	}
}

func TestMemorySearchAndReadUseSessionSnapshot(t *testing.T) {
	provider := &snapshotKnowledgeProvider{Fake: memorytest.New(), snapshotVersion: 7}
	split := privateTools(t, provider, &fakeRecallSource{})
	ctx := memory.WithSessionID(context.Background(), "s1")
	ctx = authz.WithUserID(ctx, "1")
	ctx = authz.WithAgentID(ctx, "agent1")

	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "deployment region"})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
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
	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": search.Results[0].Ref})
	if err != nil || !strings.Contains(read, "us-west") {
		t.Fatalf("snapshot memory_read: output=%s err=%v", read, err)
	}
	if provider.atVersion != 7 || provider.currentCalls != 0 {
		t.Fatalf("snapshot read: version=%d current_calls=%d", provider.atVersion, provider.currentCalls)
	}
}

// A speaker fallback must not stand in for a session user on recall: without
// one there is no authority to federate a private lane with.
func TestMemorySearchNoUserContextFailsClosed(t *testing.T) {
	split := privateTools(t, memorytest.New(), &fakeRecallSource{})
	ctx := authz.WithAgentID(context.Background(), "agent1")
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{UserID: "speaker-user"})

	_, err := split["memory_search"].Execute(ctx, map[string]any{"q": "anything"})
	if err == nil {
		t.Fatal("expected memory_search to fail without session user context")
	}
	if !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("error = %q, want no user identity", err.Error())
	}
}
