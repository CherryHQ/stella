package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxArtifactShareSize = 25 * 1024 * 1024

func (s *Server) ListArtifactShares(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	shares, err := s.q.ListArtifactShareByUser(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.ArtifactShare, 0, len(shares))
	for _, row := range shares {
		out = append(out, apitypes.ArtifactShare{
			Id:        row.ID,
			Title:     filepath.Base(row.Path),
			SessionId: row.SessionID,
			Path:      row.Path,
			MediaType: row.MediaType,
			ExpiresAt: nullStringPtr(row.ExpiresAt),
			CreatedAt: row.CreatedAt,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) CreateArtifactShare(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body apitypes.CreateArtifactShareRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.SessionId == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	root, err := s.sessionWorkspaceRoot(w, r, body.SessionId)
	if err != nil {
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fileInfo, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if fileInfo.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if fileInfo.Size() > maxArtifactShareSize {
		writeError(w, http.StatusBadRequest, "file is too large to share")
		return
	}

	mediaType := artifactMediaType(body.Path)
	if mediaType == "" {
		writeError(w, http.StatusBadRequest, "unsupported artifact type")
		return
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, tokenHash, err := newArtifactShareToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	expiresAt, err := artifactShareExpiry(body.ExpiresIn)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	share, err := s.q.CreateArtifactShare(r.Context(), sqlc.CreateArtifactShareParams{
		ID:        uuid.NewString(),
		TokenHash: tokenHash,
		UserID:    info.UserID,
		SessionID: body.SessionId,
		Path:      body.Path,
		MediaType: mediaType,
		Content:   content,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, apitypes.ArtifactShare{
		Id:        share.ID,
		Url:       artifactShareURL(r, token),
		Title:     filepath.Base(share.Path),
		SessionId: share.SessionID,
		Path:      share.Path,
		MediaType: share.MediaType,
		ExpiresAt: nullStringPtr(share.ExpiresAt),
		CreatedAt: share.CreatedAt,
	})
}

func (s *Server) RevokeArtifactShare(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	rows, err := s.q.DeleteArtifactShareByUser(r.Context(), sqlc.DeleteArtifactShareByUserParams{ID: id, UserID: info.UserID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "artifact share not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetPublicArtifactShare(w http.ResponseWriter, r *http.Request, token string) {
	share, ok := s.publicArtifactShare(w, r, token)
	if !ok {
		return
	}
	setArtifactShareContentHeaders(w, share)
	http.ServeContent(w, r, filepath.Base(share.Path), time.Time{}, bytes.NewReader(share.Content))
}

func (s *Server) publicArtifactShare(w http.ResponseWriter, r *http.Request, token string) (sqlc.ArtifactShare, bool) {
	if token == "" {
		writeError(w, http.StatusNotFound, "artifact share not found")
		return sqlc.ArtifactShare{}, false
	}
	share, err := s.q.GetArtifactShareByTokenHash(r.Context(), artifactShareTokenHash(token))
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact share not found")
		return sqlc.ArtifactShare{}, false
	}
	return share, true
}

func artifactShareURL(r *http.Request, token string) string {
	if token == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/s/%s", scheme, r.Host, token)
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

func artifactShareExpiry(preset *apitypes.CreateArtifactShareRequestExpiresIn) (sql.NullString, error) {
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

func newArtifactShareToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, artifactShareTokenHash(token), nil
}

func artifactShareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func setArtifactShareContentHeaders(w http.ResponseWriter, share sqlc.ArtifactShare) {
	w.Header().Set("Content-Type", share.MediaType)
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(filepath.Base(share.Path)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Share-Title", filepath.Base(share.Path))
	if share.ExpiresAt.Valid {
		w.Header().Set("X-Share-Expires-At", share.ExpiresAt.String)
	}
	mt := share.MediaType
	switch {
	case strings.HasPrefix(mt, "text/html"):
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; default-src 'self' https: data: blob:; img-src * data: blob:; style-src 'unsafe-inline' https:; script-src 'unsafe-inline' 'unsafe-eval' https:; connect-src https:; object-src 'none'; base-uri 'none'; form-action 'none'")
	case strings.HasPrefix(mt, "text/markdown"):
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
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
