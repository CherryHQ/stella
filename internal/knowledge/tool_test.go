package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

type recordingKnowledgeSearcher struct {
	userID  string
	agentID string
	query   string
	limit   int
	results []SearchResult
	err     error
	calls   int
}

func (s *recordingKnowledgeSearcher) Search(
	_ context.Context,
	userID, agentID, query string,
	limit int,
) ([]SearchResult, error) {
	s.calls++
	s.userID = userID
	s.agentID = agentID
	s.query = query
	s.limit = limit
	return s.results, s.err
}

func TestKnowledgeToolDefinitionHasNoIdentityFilters(t *testing.T) {
	definition := (&Tool{}).Definition()
	if definition.Name != ToolName {
		t.Fatalf("tool name = %q, want %q", definition.Name, ToolName)
	}
	properties, ok := definition.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", definition.InputSchema["properties"])
	}
	if len(properties) != 2 || properties["query"] == nil || properties["limit"] == nil {
		t.Fatalf("unexpected tool properties: %#v", properties)
	}
	query, ok := properties["query"].(map[string]any)
	if !ok || query["maxLength"] != MaxSearchQueryRunes {
		t.Fatalf("query maxLength = %#v, want %d", query["maxLength"], MaxSearchQueryRunes)
	}
	for _, forbidden := range []string{"scope", "user_id", "agent_id", "document_id", "file_ids"} {
		if _, found := properties[forbidden]; found {
			t.Fatalf("tool schema exposes forbidden property %q", forbidden)
		}
	}
}

func TestKnowledgeToolUsesTrustedContextAndReturnsOnlyPublicEvidence(t *testing.T) {
	page := uint32(3)
	searcher := &recordingKnowledgeSearcher{
		results: []SearchResult{{
			Content:  "Travel lodging is capped at 800 yuan.",
			FileName: "travel-policy.pdf",
			Locator: &PublicLocator{
				FirstPage:      &page,
				LastPage:       &page,
				HeadingContext: "Travel > Lodging",
			},
		}},
	}
	tool := &Tool{searcher: searcher}
	ctx := authz.WithUserID(context.Background(), "trusted-user")
	ctx = authz.WithAgentID(ctx, "trusted-agent")

	output, err := tool.Execute(ctx, map[string]any{
		"query":    "travel lodging",
		"scope":    "system",
		"user_id":  "forged-user",
		"agent_id": "forged-agent",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if searcher.userID != "trusted-user" || searcher.agentID != "trusted-agent" {
		t.Fatalf("search identity = %q/%q", searcher.userID, searcher.agentID)
	}
	if searcher.query != "travel lodging" || searcher.limit != 5 {
		t.Fatalf("search query/limit = %q/%d", searcher.query, searcher.limit)
	}
	for _, forbidden := range []string{
		"scope", "user_id", "agent_id", "file_id", "chunk_id", "score",
		"raw_content", "heading_path",
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output leaks %q: %s", forbidden, output)
		}
	}
	for _, required := range []string{
		`"content":"Travel lodging is capped at 800 yuan."`,
		`"file_name":"travel-policy.pdf"`,
		`"first_page":3`,
		`"heading_context":"Travel \u003e Lodging"`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("output missing %s: %s", required, output)
		}
	}
}

func TestKnowledgeToolWithoutTrustedIdentityReturnsEmpty(t *testing.T) {
	searcher := &recordingKnowledgeSearcher{}
	output, err := (&Tool{searcher: searcher}).Execute(
		context.Background(),
		map[string]any{"query": "policy"},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if output != `{"results":[]}` {
		t.Fatalf("output = %s", output)
	}
	if searcher.calls != 0 {
		t.Fatalf("search calls = %d, want 0", searcher.calls)
	}
}

func TestKnowledgeToolRejectsInvalidArguments(t *testing.T) {
	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "a1")
	tool := &Tool{searcher: &recordingKnowledgeSearcher{}}
	tests := []map[string]any{
		{"query": []any{"one", "two"}},
		{"query": "policy", "limit": 0},
		{"query": "policy", "limit": 11},
		{"query": "policy", "limit": 1.5},
		{"query": "policy", "limit": "5"},
	}
	for _, args := range tests {
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatalf("Execute(%#v) unexpectedly succeeded", args)
		}
	}
}

func TestKnowledgeToolDistinguishesSearchFailureFromNoResults(t *testing.T) {
	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "a1")

	empty, err := (&Tool{searcher: &recordingKnowledgeSearcher{}}).Execute(
		ctx,
		map[string]any{"query": "missing"},
	)
	if err != nil || empty != `{"results":[]}` {
		t.Fatalf("empty search = %q, %v", empty, err)
	}

	_, err = (&Tool{searcher: &recordingKnowledgeSearcher{
		err: errors.New("database unavailable"),
	}}).Execute(ctx, map[string]any{"query": "policy"})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("search error = %v", err)
	}
}
