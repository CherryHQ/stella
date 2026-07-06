package share

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Create and manage public share links for this user's artifacts or saved articles. Actions: artifact shares a file from the current session workspace; article shares a Recally article; list shows existing shares; revoke disables a share. For artifact, use the current session automatically and provide a workspace path. Responses include the public URL; never expose private file content unless the user asked to share it.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("share service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "share")
	if err != nil {
		return "", err
	}
	action, err := tools.ActionArg(args, "share")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, shareHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		switch {
		case errors.Is(err, ErrPathEscapes):
			return "", fmt.Errorf("path is outside the workspace — choose a file under /workspace or /user and retry")
		case errors.Is(err, ErrTooLarge):
			return "", fmt.Errorf("file is too large to share — create a smaller export and retry")
		case errors.Is(err, ErrUnsupportedType):
			return "", fmt.Errorf("unsupported artifact type — export as html, markdown, pdf, svg, or an image and retry")
		}
		return "", authz.MapError("share", err)
	}
	return tools.MarshalResult(out)
}

type shareHandler struct {
	svc   *Service
	ident authz.Identity
}

func (h shareHandler) Artifact(ctx context.Context, in ArtifactInput) (any, error) {
	created, err := h.svc.As(h.ident).ShareArtifact(ctx, memory.SessionIDFromContext(ctx), in.Path, in.Scope, in.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return shareCreatedSummary(created), nil
}

func (h shareHandler) Article(ctx context.Context, in ArticleInput) (any, error) {
	created, err := h.svc.As(h.ident).ShareArticle(ctx, in.ArticleId, in.ExpiresIn)
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
	result, err := h.svc.As(h.ident).List(ctx, limit, offset)
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
	if err := h.svc.As(h.ident).Revoke(ctx, in.Id); err != nil {
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
	out := shareResponse{ID: created.Share.ID, URL: created.URL, Title: created.Share.Title, MediaType: created.Share.MediaType, CreatedAt: created.Share.CreatedAt.UTC().Format(time.RFC3339)}
	if created.Share.ExpiresAt.Valid {
		out.ExpiresAt = created.Share.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}

func shareSummary(row sqlc.ListSharesByUserRow, url string) shareResponse {
	out := shareResponse{ID: row.ID, URL: url, Title: row.Title, MediaType: row.MediaType, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339)}
	if row.ExpiresAt.Valid {
		out.ExpiresAt = row.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}
