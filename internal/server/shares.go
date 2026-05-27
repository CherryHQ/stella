package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxShareSize = 25 * 1024 * 1024

func (s *Server) ListShares(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	rows, err := s.q.ListSharesByUser(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.Share, 0, len(rows))
	for _, row := range rows {
		out = append(out, apitypes.Share{
			Id:        row.ID,
			Title:     row.Title,
			MediaType: row.MediaType,
			ExpiresAt: nullStringPtr(row.ExpiresAt),
			CreatedAt: row.CreatedAt,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) CreateShare(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body apitypes.CreateShareRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	title, mediaType, content, err := s.resolveShareContent(w, r, info.UserID, body)
	if err != nil {
		return
	}

	token, tokenHash, err := newShareToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	expiresAt, err := shareExpiry(body.ExpiresIn)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	share, err := s.q.CreateShare(r.Context(), sqlc.CreateShareParams{
		ID:        uuid.NewString(),
		TokenHash: tokenHash,
		UserID:    info.UserID,
		Title:     title,
		MediaType: mediaType,
		Content:   content,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, apitypes.Share{
		Id:        share.ID,
		Url:       shareURL(r, token),
		Title:     share.Title,
		MediaType: share.MediaType,
		ExpiresAt: nullStringPtr(share.ExpiresAt),
		CreatedAt: share.CreatedAt,
	})
}

func (s *Server) resolveShareContent(w http.ResponseWriter, r *http.Request, userID string, body apitypes.CreateShareRequest) (title, mediaType string, content []byte, err error) {
	switch body.Source {
	case apitypes.CreateShareRequestSourceArtifact:
		return s.resolveArtifactContent(w, r, body)
	case apitypes.CreateShareRequestSourceArticle:
		return s.resolveArticleContent(w, r, userID, body)
	default:
		writeError(w, http.StatusBadRequest, "source must be one of: artifact, article")
		return "", "", nil, errors.New("invalid source")
	}
}

func (s *Server) resolveArtifactContent(w http.ResponseWriter, r *http.Request, body apitypes.CreateShareRequest) (string, string, []byte, error) {
	sessionID := strDeref(body.SessionId)
	path := strDeref(body.Path)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required for artifact shares")
		return "", "", nil, errors.New("missing session_id")
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required for artifact shares")
		return "", "", nil, errors.New("missing path")
	}

	agentID := strDeref(body.AgentId)
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required for artifact shares")
		return "", "", nil, errors.New("missing agent_id")
	}
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID)
	if err != nil {
		return "", "", nil, err
	}
	abs, err := safePath(root, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", "", nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return "", "", nil, err
	}
	if fi.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return "", "", nil, errors.New("directory")
	}
	if fi.Size() > maxShareSize {
		writeError(w, http.StatusBadRequest, "file is too large to share")
		return "", "", nil, errors.New("too large")
	}
	mt := artifactMediaType(path)
	if mt == "" {
		writeError(w, http.StatusBadRequest, "unsupported artifact type")
		return "", "", nil, errors.New("unsupported type")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", "", nil, err
	}
	return filepath.Base(path), mt, data, nil
}

func (s *Server) resolveArticleContent(w http.ResponseWriter, r *http.Request, userID string, body apitypes.CreateShareRequest) (string, string, []byte, error) {
	articleID := strDeref(body.ArticleId)
	if articleID == "" {
		writeError(w, http.StatusBadRequest, "article_id is required for article shares")
		return "", "", nil, errors.New("missing article_id")
	}
	article, err := s.recally.store.GetArticle(r.Context(), userID, articleID)
	if err != nil {
		writeError(w, http.StatusNotFound, "article not found")
		return "", "", nil, err
	}
	if article.UserID != userID {
		writeError(w, http.StatusForbidden, "not your article")
		return "", "", nil, errors.New("forbidden")
	}
	if article.FilePath == "" {
		writeError(w, http.StatusBadRequest, "article has no content")
		return "", "", nil, errors.New("no content")
	}
	filePath := article.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(config.StellaHome(), filePath)
	}
	md, err := s.recally.files.ReadArticle(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read article content")
		return "", "", nil, err
	}

	rendered, err := renderMarkdownPage(renderMarkdownOpts{
		Title:     article.Title,
		Author:    article.Author,
		SourceURL: article.URL,
		Summary:   article.Summary,
		Tags:      article.Tags,
	}, []byte(md))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render article")
		return "", "", nil, err
	}
	return article.Title, "text/html; charset=utf-8", rendered, nil
}

func (s *Server) RevokeShare(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	rows, err := s.q.DeleteShareByUser(r.Context(), sqlc.DeleteShareByUserParams{ID: id, UserID: info.UserID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
	share, err := s.q.GetShareByTokenHash(r.Context(), shareTokenHash(token))
	if err != nil {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}

	content := share.Content
	mediaType := share.MediaType
	if strings.HasPrefix(mediaType, "text/markdown") {
		expiresAt := ""
		if share.ExpiresAt.Valid {
			expiresAt = share.ExpiresAt.String
		}
		rendered, renderErr := renderMarkdownPage(renderMarkdownOpts{
			Title:     share.Title,
			ExpiresAt: expiresAt,
		}, share.Content)
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

func artifactMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".md", ".mdx", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".svg":
		return "image/svg+xml"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico":
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
		return "application/octet-stream"
	default:
		return ""
	}
}

func shareExpiry(preset *apitypes.CreateShareRequestExpiresIn) (sql.NullString, error) {
	value := "7d"
	if preset != nil && *preset != "" {
		value = string(*preset)
	}
	var d time.Duration
	switch value {
	case "1h":
		d = time.Hour
	case "1d":
		d = 24 * time.Hour
	case "7d":
		d = 7 * 24 * time.Hour
	case "never":
		return sql.NullString{}, nil
	default:
		return sql.NullString{}, fmt.Errorf("expires_in must be one of 1h, 1d, 7d, never")
	}
	return sql.NullString{String: time.Now().UTC().Add(d).Format("2006-01-02 15:04:05"), Valid: true}, nil
}

func newShareToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, shareTokenHash(token), nil
}

func shareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
		w.Header().Set("X-Share-Expires-At", share.ExpiresAt.String)
	}
	switch {
	case strings.HasPrefix(effectiveMediaType, "text/html"):
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; default-src 'self' https: data: blob:; img-src * data: blob:; style-src 'unsafe-inline' https:; script-src 'unsafe-inline' 'unsafe-eval' https:; connect-src https:; object-src 'none'; base-uri 'none'; form-action 'none'")
	default:
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	}
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

var _ apiserver.ServerInterface = (*Server)(nil)
