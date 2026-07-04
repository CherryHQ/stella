package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Authorized is the identity-scoped view of the service; all authorization
// checks live on its methods.
type Authorized struct {
	*Service
	ident authz.Identity
}

func (s *Service) As(ident authz.Identity) Authorized { return Authorized{Service: s, ident: ident} }

const MaxShareSize = 25 * 1024 * 1024

var (
	ErrPathEscapes     = errors.New("path escapes workspace root")
	ErrTooLarge        = errors.New("file is too large to share")
	ErrUnsupportedType = errors.New("unsupported artifact type")
	ErrDirectory       = errors.New("path is a directory")
	ErrNoContent       = errors.New("article has no content")
	ErrInvalidInput    = errors.New("invalid input")
)

type Service struct {
	q          *sqlc.Queries
	mem        memory.Provider
	store      *recally.Store
	files      *recally.FileManager
	stellaHome string
	baseURL    string
}

type Created struct {
	Share sqlc.Share
	Token string
	URL   string
}

type ListResult struct {
	Shares        []sqlc.ListSharesByUserRow
	NextPageToken string
}

func NewService(q *sqlc.Queries, mem memory.Provider, store *recally.Store, files *recally.FileManager, stellaHome, baseURL string) *Service {
	return &Service{q: q, mem: mem, store: store, files: files, stellaHome: stellaHome, baseURL: strings.TrimRight(baseURL, "/")}
}

func (s *Service) SetBaseURL(baseURL string) {
	if baseURL != "" {
		s.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func (s *Service) PublicURL(token string) string {
	if token == "" || s.baseURL == "" {
		return ""
	}
	return s.baseURL + "/s/" + url.PathEscape(token)
}

func (s Authorized) ShareArtifact(ctx context.Context, sessionID, path, scope, expiresIn string) (Created, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return Created{}, err
	}
	if ident.AgentID == "" {
		return Created{}, fmt.Errorf("agent_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if sessionID == "" {
		return Created{}, fmt.Errorf("session_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if path == "" {
		return Created{}, fmt.Errorf("path is required for artifact shares: %w", ErrInvalidInput)
	}

	rel := path
	if stripped, ok := strings.CutPrefix(path, pkgsandbox.MountUserData+"/"); ok {
		scope, rel = "user", stripped
	} else if stripped, ok := strings.CutPrefix(path, pkgsandbox.MountWorkspace+"/"); ok {
		scope, rel = "agent", stripped
	}
	root, err := s.sessionWorkspaceRoot(ctx, ident, sessionID, scope)
	if err != nil {
		return Created{}, err
	}
	abs, err := SafePath(root, rel)
	if err != nil {
		return Created{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Created{}, authz.ErrNotFound
	}
	if fi.IsDir() {
		return Created{}, ErrDirectory
	}
	if fi.Size() > MaxShareSize {
		return Created{}, ErrTooLarge
	}
	mt := ArtifactMediaType(path)
	if mt == "" {
		return Created{}, ErrUnsupportedType
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Created{}, err
	}
	return s.create(ctx, ident.UserID, filepath.Base(path), mt, data, expiresIn)
}

func (s Authorized) ShareArticle(ctx context.Context, articleID, expiresIn string) (Created, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return Created{}, err
	}
	if articleID == "" {
		return Created{}, fmt.Errorf("article_id is required for article shares: %w", ErrInvalidInput)
	}
	article, err := s.store.GetArticle(ctx, ident.UserID, articleID)
	if err != nil {
		return Created{}, authz.ErrNotFound
	}
	if article.UserID != ident.UserID {
		return Created{}, authz.ErrForbidden
	}
	if article.FilePath == "" {
		return Created{}, ErrNoContent
	}
	filePath := article.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(s.stellaHome, filePath)
	}
	md, err := s.files.ReadArticle(filePath)
	if err != nil {
		return Created{}, err
	}
	rendered, err := RenderMarkdownPage(RenderMarkdownOpts{Title: article.Title, Author: article.Author, SourceURL: article.URL, Summary: article.Summary, Tags: article.Tags}, []byte(md))
	if err != nil {
		return Created{}, err
	}
	return s.create(ctx, ident.UserID, article.Title, "text/html; charset=utf-8", rendered, expiresIn)
}

func (s Authorized) List(ctx context.Context, limit, offset int) (ListResult, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return ListResult{}, err
	}
	rows, err := s.q.ListSharesByUser(ctx, sqlc.ListSharesByUserParams{UserID: ident.UserID, Limit: int32(limit + 1), Offset: int32(offset)})
	if err != nil {
		return ListResult{}, err
	}
	page, next := pageRows(rows, limit, offset)
	return ListResult{Shares: page, NextPageToken: next}, nil
}

func (s Authorized) Revoke(ctx context.Context, id string) error {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return err
	}
	rows, err := s.q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: id, UserID: ident.UserID})
	if err != nil {
		return err
	}
	if rows == 0 {
		return authz.ErrNotFound
	}
	return nil
}

func (s *Service) create(ctx context.Context, userID, title, mediaType string, content []byte, expiresIn string) (Created, error) {
	token, tokenHash, err := NewToken()
	if err != nil {
		return Created{}, err
	}
	expiresAt, err := Expiry(expiresIn)
	if err != nil {
		return Created{}, err
	}
	row, err := s.q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.Must(uuid.NewV7()).String(), TokenHash: tokenHash, UserID: userID, Title: title, MediaType: mediaType, Content: content, ExpiresAt: expiresAt})
	if err != nil {
		return Created{}, err
	}
	return Created{Share: row, Token: token, URL: s.PublicURL(token)}, nil
}

func (s *Service) sessionWorkspaceRoot(ctx context.Context, ident authz.Identity, sessionID, scope string) (string, error) {
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		return "", authz.ErrNotFound
	}
	loadCtx := memory.WithUserID(ctx, ident.UserID)
	if ident.AgentID != "" {
		loadCtx = memory.WithAgentID(loadCtx, ident.AgentID)
	}
	si, err := sm.LoadInfo(loadCtx, sessionID)
	if err != nil {
		return "", authz.ErrNotFound
	}
	if si.UserID != ident.UserID {
		return "", authz.ErrForbidden
	}
	if ident.AgentID != "" && si.AgentID != ident.AgentID {
		if ident.AgentScoped {
			return "", authz.ErrForbidden
		}
		return "", authz.ErrNotFound
	}
	if si.UserID == "" || si.AgentID == "" {
		return "", authz.ErrNotFound
	}
	if _, err := agent.SetupUserWorkspace(s.stellaHome, si.UserID, si.AgentID); err != nil {
		return "", err
	}
	return WorkspaceRootForScope(s.stellaHome, si.UserID, si.AgentID, scope), nil
}

func WorkspaceRootForScope(stellaHome, userID, agentID, scope string) string {
	if scope == "user" {
		return agent.UserDataDir(agent.UserHomeDir(stellaHome, userID))
	}
	return agent.UserAgentDir(stellaHome, userID, agentID)
}

func SafePath(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", ErrPathEscapes
	}
	return abs, nil
}

func ArtifactMediaType(path string) string {
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

func Expiry(preset string) (pgtype.Timestamptz, error) {
	value := "7d"
	if preset != "" {
		value = preset
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
		return pgtype.Timestamptz{}, nil
	default:
		return pgtype.Timestamptz{}, fmt.Errorf("expires_in must be one of 1h, 1d, 7d, never: %w", ErrInvalidInput)
	}
	return pgtype.Timestamptz{Time: time.Now().UTC().Add(d), Valid: true}, nil
}

func NewToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, TokenHash(token), nil
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
