package library

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestLibrarySearchToolNameIsProviderCompatible(t *testing.T) {
	if ToolName != "library_search" {
		t.Fatalf("ToolName = %q, want library_search", ToolName)
	}
	// OpenAI-compatible function calling permits only letters, digits,
	// underscores, and dashes, with a maximum length of 64 characters.
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(ToolName) {
		t.Fatalf("ToolName = %q, want an OpenAI-compatible function name", ToolName)
	}
}

func TestLibrarySearchToolSchemaHasOnlyQueryAndLimit(t *testing.T) {
	definition := NewTool(&Service{}).Definition()
	if definition.Name != ToolName {
		t.Fatalf("tool name = %q, want %q", definition.Name, ToolName)
	}
	properties, ok := definition.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", definition.InputSchema["properties"])
	}
	if len(properties) != 2 || properties["query"] == nil || properties["limit"] == nil {
		t.Fatalf("tool properties = %#v, want only query and limit", properties)
	}
	if additional, _ := definition.InputSchema["additionalProperties"].(bool); additional {
		t.Fatal("tool schema permits additional properties")
	}
}

func TestLibrarySearchToolRejectsInvalidInputBeforeIdentityLookup(t *testing.T) {
	tool := NewTool(&Service{})
	for name, args := range map[string]map[string]any{
		"query array":   {"query": []any{"one", "two"}},
		"file id":       {"query": "one", "file_id": "file-1"},
		"identity":      {"query": "one", "user_id": "user-1"},
		"load action":   {"query": "one", "action": "load"},
		"fetch locator": {"query": "one", "locator": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Execute(t.Context(), args); !errors.Is(err, ErrInvalidSearch) {
				t.Fatalf("Execute error = %v, want ErrInvalidSearch", err)
			}
		})
	}
}

func TestLibrarySearchToolReturnsServiceValidationError(t *testing.T) {
	tool := NewTool(&Service{q: &sqlc.Queries{}})
	ctx := authz.WithAgentID(authz.WithUserID(t.Context(), "user-1"), "agent-1")
	if _, err := tool.Execute(ctx, map[string]any{"query": "   ", "limit": MaxSearchLimit + 1}); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("Execute error = %v, want ErrInvalidSearch", err)
	}
}

func TestPublicSearchLocatorDropsInternalByteOffsets(t *testing.T) {
	locator, err := publicSearchLocator([]byte(`{
		"first_page":2,
		"last_page":3,
		"row_start":4,
		"row_end":8,
		"heading_path":["Policy"],
		"byte_start":100,
		"byte_end":200
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if locator == nil || locator.FirstPage == nil || *locator.FirstPage != 2 ||
		locator.RowStart == nil || *locator.RowStart != 4 || locator.RowEnd == nil || *locator.RowEnd != 8 ||
		strings.Join(locator.HeadingPath, "/") != "Policy" {
		t.Fatalf("locator = %+v", locator)
	}
}

func TestPublicSearchLocatorRejectsInvalidRowRanges(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"missing end": `{"row_start":1,"byte_start":0,"byte_end":1}`,
		"zero start":  `{"row_start":0,"row_end":1,"byte_start":0,"byte_end":1}`,
		"reversed":    `{"row_start":4,"row_end":3,"byte_start":0,"byte_end":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := publicSearchLocator([]byte(raw)); err == nil {
				t.Fatal("invalid row range was accepted")
			}
		})
	}
}

func TestLibrarySearchValidatesQueryAndLimitBeforeDatabaseCall(t *testing.T) {
	authority, err := authz.NewAgentAuthority("user-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	// q is deliberately non-nil but unusable: every invalid request must return
	// before reaching SQL.
	service := &Service{q: &sqlc.Queries{}}
	for name, test := range map[string]struct {
		query string
		limit int
	}{
		"empty query":    {query: "   ", limit: 1},
		"query too long": {query: strings.Repeat("知", MaxSearchQueryRunes+1), limit: 1},
		"limit too low":  {query: "policy", limit: -1},
		"limit too high": {query: "policy", limit: MaxSearchLimit + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Search(t.Context(), authority, test.query, test.limit); !errors.Is(err, ErrInvalidSearch) {
				t.Fatalf("Search error = %v, want ErrInvalidSearch", err)
			}
		})
	}
}
