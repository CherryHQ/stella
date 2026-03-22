package feishutool

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larksearch "github.com/larksuite/oapi-sdk-go/v3/service/search/v2"
	"github.com/vaayne/anna/internal/toolspec"
)

var searchInputSchema = mustParseSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["search_docs"],
      "description": "The action to perform"
    },
    "query": {
      "type": "string",
      "description": "Search keyword. Empty string returns results sorted by recent access."
    },
    "doc_types": {
      "type": "array",
      "items": {"type": "string", "enum": ["DOC", "SHEET", "BITABLE", "MINDNOTE", "FILE", "WIKI", "DOCX", "FOLDER", "SLIDES"]},
      "description": "Filter by document types (optional)"
    },
    "creator_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Filter by creator OpenIDs (optional, max 20)"
    },
    "only_title": {
      "type": "boolean",
      "description": "Search only in titles (default: false, searches title and body)"
    },
    "sort_type": {
      "type": "string",
      "enum": ["DEFAULT_TYPE", "OPEN_TIME", "EDIT_TIME", "EDIT_TIME_ASC", "CREATE_TIME"],
      "description": "Sort order. EDIT_TIME=newest edits first (recommended), CREATE_TIME=by creation time."
    },
    "page_size": {
      "type": "number",
      "description": "Page size (default 15, max 20)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`)

// SearchTool provides Feishu document and wiki search.
type SearchTool struct {
	client *Client
}

// NewSearchTool creates a feishu_search tool.
func NewSearchTool(client *Client) *SearchTool {
	return &SearchTool{client: client}
}

func (t *SearchTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_search",
		Description: `Search Feishu/Lark documents and wikis. Uses user token when available.

Searches across all cloud documents and wiki pages accessible to the user.

Actions:
- search_docs: Global document and wiki search. Optional: query (empty = recent docs), doc_types, creator_ids, only_title, sort_type, page_size, page_token.

Results include title and summary with highlighted matching keywords (wrapped in <h> tags).
Supports filtering by document type (DOC, SHEET, WIKI, DOCX, BITABLE, etc.), creator, and sort order.`,
		InputSchema: searchInputSchema,
	}
}

func (t *SearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "search_docs":
		return t.searchDocs(ctx, args)
	default:
		return "", fmt.Errorf("feishu_search: unknown action %q", action)
	}
}

func (t *SearchTool) searchDocs(ctx context.Context, args map[string]any) (string, error) {
	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		query := stringArg(args, "query")

		bodyBuilder := larksearch.NewSearchDocWikiReqBodyBuilder().
			Query(query)

		if ps := intArg(args, "page_size"); ps > 0 {
			bodyBuilder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			bodyBuilder.PageToken(pt)
		}

		// Build doc filter.
		docFilter := buildSearchFilter(args)
		bodyBuilder.DocFilter(docFilter)
		// Apply the same filter to wiki.
		wikiFilter := buildSearchWikiFilter(args)
		bodyBuilder.WikiFilter(wikiFilter)

		resp, err := t.client.Lark().Search.DocWiki.Search(ctx,
			larksearch.NewSearchDocWikiReqBuilder().
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("search docs: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("search docs: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = paginatedResultMap("results", resp.Data.ResUnits, resp.Data.HasMore, resp.Data.PageToken)
		if resp.Data.Total != nil {
			result["total"] = *resp.Data.Total
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_search search_docs: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func buildSearchFilter(args map[string]any) *larksearch.DocFilter {
	builder := larksearch.NewDocFilterBuilder()
	if types := toStringSlice(args, "doc_types"); len(types) > 0 {
		builder.DocTypes(types)
	}
	if ids := toStringSlice(args, "creator_ids"); len(ids) > 0 {
		builder.CreatorIds(ids)
	}
	if v, ok := boolArg(args, "only_title"); ok {
		builder.OnlyTitle(v)
	}
	if sort := stringArg(args, "sort_type"); sort != "" {
		builder.SortType(sort)
	}
	return builder.Build()
}

func buildSearchWikiFilter(args map[string]any) *larksearch.WikiFilter {
	builder := larksearch.NewWikiFilterBuilder()
	if types := toStringSlice(args, "doc_types"); len(types) > 0 {
		builder.DocTypes(types)
	}
	if ids := toStringSlice(args, "creator_ids"); len(ids) > 0 {
		builder.CreatorIds(ids)
	}
	if v, ok := boolArg(args, "only_title"); ok {
		builder.OnlyTitle(v)
	}
	if sort := stringArg(args, "sort_type"); sort != "" {
		builder.SortType(sort)
	}
	return builder.Build()
}
