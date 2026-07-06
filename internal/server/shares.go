package server

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Server) ListShares(w http.ResponseWriter, r *http.Request, params apiserver.ListSharesParams) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	result, err := s.shareSvc.As(shareIdentity(info, "")).List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	out := make([]apitypes.Share, 0, len(result.Shares))
	for _, row := range result.Shares {
		out = append(out, apiShare(row, ""))
	}
	resp := map[string]any{"shares": out}
	if result.NextPageToken != "" {
		resp["next_page_token"] = result.NextPageToken
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) CreateShare(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body apitypes.CreateShareRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	created, err := s.createShare(r, info, body)
	if err != nil {
		s.writeShareError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apiCreatedShare(created.Share, shareURL(r, created.Token)))
}

func (s *Server) createShare(r *http.Request, info *AuthInfo, body apitypes.CreateShareRequest) (sharepkg.Created, error) {
	expiresIn := ""
	if body.ExpiresIn != nil {
		expiresIn = string(*body.ExpiresIn)
	}
	switch body.Source {
	case apitypes.CreateShareRequestSourceArtifact:
		sessionID := strDeref(body.SessionId)
		path := strDeref(body.Path)
		agentID := strDeref(body.AgentId)
		if agentID == "" {
			return sharepkg.Created{}, fmt.Errorf("agent_id is required for artifact shares: %w", sharepkg.ErrInvalidInput)
		}
		scope := ""
		if body.Scope != nil {
			scope = string(*body.Scope)
		}
		return s.shareSvc.As(shareIdentity(info, agentID)).ShareArtifact(r.Context(), sessionID, path, scope, expiresIn)
	case apitypes.CreateShareRequestSourceArticle:
		return s.shareSvc.As(shareIdentity(info, "")).ShareArticle(r.Context(), strDeref(body.ArticleId), expiresIn)
	default:
		return sharepkg.Created{}, fmt.Errorf("source must be one of: artifact, article: %w", sharepkg.ErrInvalidInput)
	}
}

func (s *Server) RevokeShare(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err := s.shareSvc.As(shareIdentity(info, "")).Revoke(r.Context(), id); err != nil {
		if authz.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "share not found")
			return
		}
		s.writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetShareContent(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}
	share, err := s.q.GetShareByTokenHash(r.Context(), sharepkg.TokenHash(token))
	if err != nil {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}

	content := share.Content
	mediaType := share.MediaType
	if strings.HasPrefix(mediaType, "text/markdown") {
		expiresAt := ""
		if share.ExpiresAt.Valid {
			expiresAt = share.ExpiresAt.Time.UTC().Format(time.RFC3339)
		}
		rendered, renderErr := sharepkg.RenderMarkdownPage(sharepkg.RenderMarkdownOpts{Title: share.Title, ExpiresAt: expiresAt}, share.Content)
		if renderErr == nil {
			content = rendered
			mediaType = "text/html; charset=utf-8"
		} else {
			slog.Warn("failed to render markdown share", "share_id", share.ID, "error", renderErr)
		}
	}

	setShareContentHeaders(w, share, mediaType)
	http.ServeContent(w, r, share.Title, time.Time{}, bytes.NewReader(content))
}

func shareIdentity(info *AuthInfo, agentID string) authz.Identity {
	ident := toolIdentity(info)
	if !ident.AgentScoped {
		ident.AgentID = agentID
	}
	return ident
}

func (s *Server) writeShareError(w http.ResponseWriter, err error) {
	switch {
	case authz.IsForbidden(err):
		writeError(w, http.StatusForbidden, "permission denied")
	case authz.IsNotFound(err):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, sharepkg.ErrDirectory):
		writeError(w, http.StatusBadRequest, "path is a directory")
	case errors.Is(err, sharepkg.ErrTooLarge):
		writeError(w, http.StatusBadRequest, "file is too large to share")
	case errors.Is(err, sharepkg.ErrUnsupportedType):
		writeError(w, http.StatusBadRequest, "unsupported artifact type")
	case errors.Is(err, sharepkg.ErrPathEscapes):
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, sharepkg.ErrNoContent):
		writeError(w, http.StatusBadRequest, "article has no content")
	case errors.Is(err, sharepkg.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid request")
	default:
		s.writeInternalError(w, err)
	}
}

func apiShare(row sqlc.ListSharesByUserRow, url string) apitypes.Share {
	return apitypes.Share{Id: row.ID, Url: url, Title: row.Title, MediaType: row.MediaType, ExpiresAt: parseTimePtr(row.ExpiresAt), CreatedAt: row.CreatedAt.UTC()}
}

func apiCreatedShare(row sqlc.Share, url string) apitypes.Share {
	return apitypes.Share{Id: row.ID, Url: url, Title: row.Title, MediaType: row.MediaType, ExpiresAt: parseTimePtr(row.ExpiresAt), CreatedAt: row.CreatedAt.UTC()}
}

func shareURL(r *http.Request, token string) string {
	if token == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return fmt.Sprintf("%s://%s/s/%s", scheme, host, token)
}

func setShareContentHeaders(w http.ResponseWriter, share sqlc.Share, effectiveMediaType string) {
	w.Header().Set("Content-Type", effectiveMediaType)
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(share.Title))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Share-Title", share.Title)
	w.Header().Set("X-Share-Media-Type", share.MediaType)
	if share.ExpiresAt.Valid {
		w.Header().Set("X-Share-Expires-At", share.ExpiresAt.Time.UTC().Format(time.RFC3339))
	}
	switch {
	case strings.HasPrefix(effectiveMediaType, "text/html"):
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; default-src 'self' https: data: blob:; img-src * data: blob:; style-src 'unsafe-inline' https:; script-src 'unsafe-inline' 'unsafe-eval' https:; connect-src https:; object-src 'none'; base-uri 'none'; form-action 'none'")
	default:
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	}
}

var _ apiserver.ServerInterface = (*Server)(nil)
