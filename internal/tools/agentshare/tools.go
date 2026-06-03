// Package agentshare exposes public-share creation as native agent tools.
// Shares are scoped to the acting user/agent (read from context). The
// security-critical invariants (path-traversal guard, size cap, media-type
// allowlist, article ownership) live in internal/share, which these tools call.
package agentshare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewTools builds the share native tools bound to the given service. Returns nil
// when the service is unavailable so callers can append unconditionally.
func NewTools(svc *share.Service) []tools.Tool {
	if svc == nil {
		return nil
	}
	t := &impl{svc: svc, home: config.StellaHome()}
	return []tools.Tool{
		fnTool{artifactDef(), t.artifact},
		fnTool{articleDef(), t.article},
	}
}

type impl struct {
	svc  *share.Service
	home string
}

func (t *impl) artifact(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	root, err := agent.SetupUserWorkspace(agentID, t.home, userID)
	if err != nil {
		return "", err
	}
	content, err := t.svc.ArtifactContent(root, path)
	if err != nil {
		return "", shareErr(err)
	}
	res, err := t.svc.Create(ctx, userID, content, expiresIn(args))
	if err != nil {
		return "", shareErr(err)
	}
	return t.marshalResult(res)
}

func (t *impl) article(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	articleID, _ := args["article_id"].(string)
	if articleID == "" {
		return "", fmt.Errorf("article_id is required")
	}
	res, err := t.svc.CreateArticleShare(ctx, userID, articleID, expiresIn(args))
	if err != nil {
		return "", shareErr(err)
	}
	return t.marshalResult(res)
}

func (t *impl) marshalResult(res share.Result) (string, error) {
	out := struct {
		URL       string `json:"url"`
		Title     string `json:"title"`
		MediaType string `json:"media_type"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}{
		URL:       t.svc.PublicURL(res.Token),
		Title:     res.Title,
		MediaType: res.MediaType,
	}
	if res.ExpiresAt.Valid {
		out.ExpiresAt = res.ExpiresAt.String
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func expiresIn(args map[string]any) string {
	v, _ := args["expires_in"].(string)
	return v
}

// shareErr surfaces the user-actionable share sentinels verbatim while letting
// unexpected internal errors propagate as-is.
func shareErr(err error) error {
	for _, s := range []error{
		share.ErrInvalidPath, share.ErrNotFound, share.ErrIsDir, share.ErrTooLarge,
		share.ErrUnsupportedType, share.ErrForbidden, share.ErrNoContent,
		share.ErrInvalidExpiry, share.ErrArticles,
	} {
		if errors.Is(err, s) {
			return s
		}
	}
	return err
}

type fnTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t fnTool) Definition() tools.Definition { return t.def }
func (t fnTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}
