package server

import (
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
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxArtifactShareSize = 25 * 1024 * 1024

type artifactKind struct {
	kind      apitypes.ArtifactShareKind
	mediaType string
}

func (s *Server) ListArtifactShares(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	shares, err := s.q.ListArtifactShareByOwner(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.ArtifactShare, 0, len(shares))
	for _, share := range shares {
		out = append(out, artifactShareResponse(r, share, ""))
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

	kind, ok := classifyArtifactShare(body.Path)
	if !ok {
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
		ID:              uuid.NewString(),
		TokenHash:       tokenHash,
		OwnerUserID:     info.UserID,
		SourceSessionID: body.SessionId,
		SourcePath:      body.Path,
		Title:           filepath.Base(body.Path),
		MediaType:       kind.mediaType,
		Kind:            string(kind.kind),
		Content:         content,
		SizeBytes:       int64(len(content)),
		ExpiresAt:       expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, artifactShareResponse(r, share, token))
}

func (s *Server) RevokeArtifactShare(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	rows, err := s.q.RevokeArtifactShareByOwner(r.Context(), sqlc.RevokeArtifactShareByOwnerParams{ID: id, OwnerUserID: info.UserID})
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
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func (s *Server) GetPublicArtifactShareContent(w http.ResponseWriter, r *http.Request, token string) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func artifactShareResponse(r *http.Request, share sqlc.ArtifactShare, token string) apitypes.ArtifactShare {
	expiresAt := nullStringPtr(share.ExpiresAt)
	lastAccessedAt := nullStringPtr(share.LastAccessedAt)
	return apitypes.ArtifactShare{
		Id:              share.ID,
		Url:             artifactShareURL(r, token),
		Title:           share.Title,
		SourceSessionId: share.SourceSessionID,
		SourcePath:      share.SourcePath,
		MediaType:       share.MediaType,
		Kind:            apitypes.ArtifactShareKind(share.Kind),
		SizeBytes:       share.SizeBytes,
		ExpiresAt:       expiresAt,
		Revoked:         share.RevokedAt.Valid,
		CreatedAt:       share.CreatedAt,
		LastAccessedAt:  lastAccessedAt,
	}
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

func classifyArtifactShare(path string) (artifactKind, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return artifactKind{kind: apitypes.ArtifactShareKindHtml, mediaType: "text/html; charset=utf-8"}, true
	case ".md", ".mdx", ".markdown":
		return artifactKind{kind: apitypes.ArtifactShareKindMarkdown, mediaType: "text/markdown; charset=utf-8"}, true
	case ".pdf":
		return artifactKind{kind: apitypes.ArtifactShareKindPdf, mediaType: "application/pdf"}, true
	case ".svg":
		return artifactKind{kind: apitypes.ArtifactShareKindImage, mediaType: "image/svg+xml"}, true
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico":
		mediaType := mime.TypeByExtension(ext)
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		return artifactKind{kind: apitypes.ArtifactShareKindImage, mediaType: mediaType}, true
	default:
		return artifactKind{}, false
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
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

var _ apiserver.ServerInterface = (*Server)(nil)
