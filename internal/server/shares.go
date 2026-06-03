package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/share"
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
	rows, err := s.q.ListSharesByUser(r.Context(), sqlc.ListSharesByUserParams{
		UserID: info.UserID,
		Limit:  int64(limit + 1),
		Offset: int64(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	page, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Share, 0, len(page))
	for _, row := range page {
		out = append(out, apitypes.Share{
			Id:        row.ID,
			Title:     row.Title,
			MediaType: row.MediaType,
			ExpiresAt: parseTimePtr(row.ExpiresAt),
			CreatedAt: parseTime(row.CreatedAt),
		})
	}
	resp := map[string]any{"shares": out}
	if nextToken != "" {
		resp["next_page_token"] = nextToken
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

	content, ok := s.resolveShareContent(w, r, info.UserID, body)
	if !ok {
		return
	}

	res, err := s.shares.Create(r.Context(), info.UserID, content, shareExpiresIn(body.ExpiresIn))
	if err != nil {
		if errors.Is(err, share.ErrInvalidExpiry) {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apitypes.Share{
		Id:        res.ID,
		Url:       shareURL(r, res.Token),
		Title:     res.Title,
		MediaType: res.MediaType,
		ExpiresAt: parseTimePtr(res.ExpiresAt),
		CreatedAt: parseTime(res.CreatedAt),
	})
}

// resolveShareContent builds the shareable content for the request, writing the
// appropriate HTTP error and returning ok=false on failure.
func (s *Server) resolveShareContent(w http.ResponseWriter, r *http.Request, userID string, body apitypes.CreateShareRequest) (share.Content, bool) {
	switch body.Source {
	case apitypes.CreateShareRequestSourceArtifact:
		return s.resolveArtifactContent(w, r, body)
	case apitypes.CreateShareRequestSourceArticle:
		return s.resolveArticleContent(w, r, userID, body)
	default:
		writeError(w, http.StatusBadRequest, "source must be one of: artifact, article")
		return share.Content{}, false
	}
}

func (s *Server) resolveArtifactContent(w http.ResponseWriter, r *http.Request, body apitypes.CreateShareRequest) (share.Content, bool) {
	sessionID := strDeref(body.SessionId)
	path := strDeref(body.Path)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required for artifact shares")
		return share.Content{}, false
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required for artifact shares")
		return share.Content{}, false
	}
	agentID := strDeref(body.AgentId)
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required for artifact shares")
		return share.Content{}, false
	}
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID)
	if err != nil {
		return share.Content{}, false
	}
	content, err := s.shares.ArtifactContent(root, path)
	if err != nil {
		writeArtifactError(w, err)
		return share.Content{}, false
	}
	return content, true
}

func (s *Server) resolveArticleContent(w http.ResponseWriter, r *http.Request, userID string, body apitypes.CreateShareRequest) (share.Content, bool) {
	articleID := strDeref(body.ArticleId)
	if articleID == "" {
		writeError(w, http.StatusBadRequest, "article_id is required for article shares")
		return share.Content{}, false
	}
	content, err := s.RenderArticle(r.Context(), userID, articleID)
	if err != nil {
		writeArticleError(w, err)
		return share.Content{}, false
	}
	return content, true
}

// RenderArticle implements share.ArticleRenderer: it loads the user's article,
// enforces ownership, and renders it to shareable HTML.
func (s *Server) RenderArticle(ctx context.Context, userID, articleID string) (share.Content, error) {
	article, err := s.recally.store.GetArticle(ctx, userID, articleID)
	if err != nil {
		return share.Content{}, share.ErrNotFound
	}
	if article.UserID != userID {
		return share.Content{}, share.ErrForbidden
	}
	if article.FilePath == "" {
		return share.Content{}, share.ErrNoContent
	}
	filePath := article.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(config.StellaHome(), filePath)
	}
	md, err := s.recally.files.ReadArticle(filePath)
	if err != nil {
		return share.Content{}, fmt.Errorf("read article content: %w", err)
	}
	rendered, err := renderMarkdownPage(renderMarkdownOpts{
		Title:     article.Title,
		Author:    article.Author,
		SourceURL: article.URL,
		Summary:   article.Summary,
		Tags:      article.Tags,
	}, []byte(md))
	if err != nil {
		return share.Content{}, fmt.Errorf("render article: %w", err)
	}
	return share.Content{Title: article.Title, MediaType: "text/html; charset=utf-8", Data: rendered}, nil
}

// writeArtifactError maps share sentinels onto the original artifact HTTP codes.
func writeArtifactError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, share.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, share.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, share.ErrIsDir):
		writeError(w, http.StatusBadRequest, "path is a directory")
	case errors.Is(err, share.ErrTooLarge):
		writeError(w, http.StatusBadRequest, "file is too large to share")
	case errors.Is(err, share.ErrUnsupportedType):
		writeError(w, http.StatusBadRequest, "unsupported artifact type")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// writeArticleError maps share sentinels onto the original article HTTP codes.
func writeArticleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, share.ErrNotFound):
		writeError(w, http.StatusNotFound, "article not found")
	case errors.Is(err, share.ErrForbidden):
		writeError(w, http.StatusForbidden, "not your article")
	case errors.Is(err, share.ErrNoContent):
		writeError(w, http.StatusBadRequest, "article has no content")
	default:
		writeError(w, http.StatusInternalServerError, "failed to render article")
	}
}

func (s *Server) RevokeShare(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	rows, err := s.q.DeleteShareByUser(r.Context(), sqlc.DeleteShareByUserParams{ID: id, UserID: info.UserID})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetShareContent(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}
	sh, err := s.q.GetShareByTokenHash(r.Context(), share.TokenHash(token))
	if err != nil {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}

	content := sh.Content
	mediaType := sh.MediaType
	if strings.HasPrefix(mediaType, "text/markdown") {
		expiresAt := ""
		if sh.ExpiresAt.Valid {
			expiresAt = sh.ExpiresAt.String
		}
		rendered, renderErr := renderMarkdownPage(renderMarkdownOpts{
			Title:     sh.Title,
			ExpiresAt: expiresAt,
		}, sh.Content)
		if renderErr == nil {
			content = rendered
			mediaType = "text/html; charset=utf-8"
		} else {
			slog.Warn("failed to render markdown share", "share_id", sh.ID, "error", renderErr)
		}
	}

	setShareContentHeaders(w, sh, mediaType)
	http.ServeContent(w, r, sh.Title, time.Time{}, bytes.NewReader(content))
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

// shareExpiresIn normalizes the optional API preset into the string the share
// service understands ("" defaults to 7d).
func shareExpiresIn(preset *apitypes.CreateShareRequestExpiresIn) string {
	if preset == nil {
		return ""
	}
	return string(*preset)
}

func setShareContentHeaders(w http.ResponseWriter, sh sqlc.Share, effectiveMediaType string) {
	w.Header().Set("Content-Type", effectiveMediaType)
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(sh.Title))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Share-Title", sh.Title)
	w.Header().Set("X-Share-Media-Type", sh.MediaType)
	if sh.ExpiresAt.Valid {
		w.Header().Set("X-Share-Expires-At", sh.ExpiresAt.String)
	}
	switch {
	case strings.HasPrefix(effectiveMediaType, "text/html"):
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; default-src 'self' https: data: blob:; img-src * data: blob:; style-src 'unsafe-inline' https:; script-src 'unsafe-inline' 'unsafe-eval' https:; connect-src https:; object-src 'none'; base-uri 'none'; form-action 'none'")
	default:
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	}
}

var _ apiserver.ServerInterface = (*Server)(nil)
