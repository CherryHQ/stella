package share

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
)

// ListTool is the share action that lists what this agent can reach. Error
// prose points at it, so a rename shows up here rather than in a string.
const ListTool = "share_list"

// actionDescriptions is the model-facing description per generated tool. A
// split tool's schema is exact, so each description only says what the call
// does and what it costs.
var actionDescriptions = map[string]string{
	"artifact_create": "Publish one file from the current session workspace at a public URL and return that URL. Anyone with the link can read it, so only share a path the user asked to share.",
	"article_create":  "Publish one saved Recally article at a public URL and return that URL. Anyone with the link can read it, so only share an article the user asked to share.",
	"list":            "List this user's existing share links with their targets and expiry. Never returns the shared content itself.",
	"revoke":          "Disable one share link by id. The URL stops resolving immediately and cannot be re-enabled; create a new share instead.",
}

// Tool is one generated share action. The tool name carries the action, so the
// provider validates arguments against an exact schema before dispatch.
type Tool struct {
	spec ActionTool
	svc  *Service
}

// NewTool builds one share action tool.
func NewTool(svc *Service, spec ActionTool) *Tool { return &Tool{spec: spec, svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return t.spec.Definition(actionDescriptions[t.spec.Action])
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("share service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, t.spec.Name)
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	out, err := Dispatch(ctx, shareHandler{svc: t.svc, authority: authority}, t.spec.Action, args)
	if err != nil {
		switch {
		case errors.Is(err, ErrPathEscapes), errors.Is(err, ErrInvalidArtifactPath):
			return "", fmt.Errorf("artifact path must be relative or start with $HOME or $STELLA_ASSETS_DIR")
		case errors.Is(err, ErrTooLarge):
			return "", fmt.Errorf("file is too large to share — create a smaller export and retry")
		case errors.Is(err, ErrUnsupportedType):
			return "", fmt.Errorf("unsupported artifact type — export as html, markdown, pdf, svg, or an image and retry")
		}
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	return tools.MarshalResult(out)
}

type shareHandler struct {
	svc       *Service
	authority authz.Authority
}

func (h shareHandler) access() (*Access, error) {
	return h.svc.Access(h.authority)
}

func (h shareHandler) ArtifactCreate(ctx context.Context, in ArtifactCreateInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	// An agent tool is confined to its own workspace: the bound agent selects it.
	created, err := acc.ShareArtifact(ctx, memory.SessionIDFromContext(ctx), in.Path, in.Scope, acc.agentID, in.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return shareCreatedSummary(created), nil
}

func (h shareHandler) ArticleCreate(ctx context.Context, in ArticleCreateInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	created, err := acc.ShareArticle(ctx, in.ArticleId, in.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return shareCreatedSummary(created), nil
}

func (h shareHandler) List(ctx context.Context, in ListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	result, err := acc.List(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]shareResponse, 0, len(result.Shares))
	for _, row := range result.Shares {
		items = append(items, shareSummary(row, ""))
	}
	return listResponse[shareResponse]{Items: items, HasMore: result.NextPageToken != "", NextPageToken: result.NextPageToken}, nil
}

func (h shareHandler) Revoke(ctx context.Context, in RevokeInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	if err := acc.Revoke(ctx, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "revoked"}, nil
}

type shareResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func shareCreatedSummary(created Created) shareResponse {
	return shareSummary(created.Share, created.URL)
}

func shareSummary(row Share, url string) shareResponse {
	out := shareResponse{ID: row.ID, URL: url, Title: row.Title, MediaType: row.MediaType, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339)}
	if row.ExpiresAt != nil {
		out.ExpiresAt = row.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}
