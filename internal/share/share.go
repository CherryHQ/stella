// Package share owns the surface-independent core of creating public shares:
// reading and validating a workspace artifact, rendering a user's article,
// minting a share token, and persisting the share. The HTTP handler and the
// native agent tool both delegate here so the security-critical invariants
// (path-traversal guard, size cap, media-type allowlist, article ownership)
// live in exactly one place. Access control (which session/user may reach a
// resource) is the caller's responsibility — see ArtifactContent's contract.
package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// MaxSize is the largest artifact that may be shared.
const MaxSize = 25 * 1024 * 1024

// Sentinel errors let each caller map a failure onto its own surface (HTTP
// status code or tool error string) without the two surfaces depending on each
// other.
var (
	ErrInvalidPath     = errors.New("invalid path")
	ErrNotFound        = errors.New("not found")
	ErrIsDir           = errors.New("path is a directory")
	ErrTooLarge        = errors.New("file is too large to share")
	ErrUnsupportedType = errors.New("unsupported artifact type")
	ErrForbidden       = errors.New("not your article")
	ErrNoContent       = errors.New("article has no content")
	ErrInvalidExpiry   = errors.New("expires_in must be one of 1h, 1d, 7d, never")
	ErrArticles        = errors.New("article sharing is unavailable")
)

// Content is the resolved, shareable payload before it is persisted.
type Content struct {
	Title     string
	MediaType string
	Data      []byte
}

// Result describes a created share. Token is the secret URL component; callers
// build the public URL via PublicURL (or, for HTTP, from the inbound request).
type Result struct {
	ID        string
	Token     string
	Title     string
	MediaType string
	ExpiresAt sql.NullString
	CreatedAt string
}

// Store is the slice of generated queries the service persists through.
type Store interface {
	CreateShare(ctx context.Context, arg sqlc.CreateShareParams) (sqlc.Share, error)
}

// ArticleRenderer renders a user's saved article into shareable HTML. It is
// implemented outside this package (the server, which owns recally + the
// markdown renderer) and injected so article ownership stays its concern.
// Implementations must return ErrNotFound / ErrForbidden / ErrNoContent.
type ArticleRenderer interface {
	RenderArticle(ctx context.Context, userID, articleID string) (Content, error)
}

type Service struct {
	store    Store
	articles ArticleRenderer
	baseURL  string
}

func NewService(store Store, articles ArticleRenderer, baseURL string) *Service {
	return &Service{store: store, articles: articles, baseURL: baseURL}
}

// PublicURL builds the externally reachable URL for a share token using the
// configured base URL. HTTP callers may instead derive the URL from the inbound
// request to honor forwarded host/proto headers.
func (s *Service) PublicURL(token string) string {
	if token == "" {
		return ""
	}
	return strings.TrimRight(s.baseURL, "/") + "/s/" + token
}

// ArtifactContent reads and validates a workspace file for sharing. root MUST be
// an already-resolved, access-checked workspace root that the caller is
// authorized to read; ArtifactContent only guards against escaping root, not
// against who owns root.
func (s *Service) ArtifactContent(root, path string) (Content, error) {
	if path == "" {
		return Content{}, ErrInvalidPath
	}
	abs, err := SafePath(root, path)
	if err != nil {
		return Content{}, ErrInvalidPath
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Content{}, ErrNotFound
	}
	if fi.IsDir() {
		return Content{}, ErrIsDir
	}
	if fi.Size() > MaxSize {
		return Content{}, ErrTooLarge
	}
	mt := MediaType(path)
	if mt == "" {
		return Content{}, ErrUnsupportedType
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Content{}, err
	}
	return Content{Title: filepath.Base(path), MediaType: mt, Data: data}, nil
}

// CreateArticleShare renders the user's article and persists it as a share.
func (s *Service) CreateArticleShare(ctx context.Context, userID, articleID, expiresIn string) (Result, error) {
	if s.articles == nil {
		return Result{}, ErrArticles
	}
	c, err := s.articles.RenderArticle(ctx, userID, articleID)
	if err != nil {
		return Result{}, err
	}
	return s.Create(ctx, userID, c, expiresIn)
}

// Create mints a token and persists the share, returning its metadata.
func (s *Service) Create(ctx context.Context, userID string, c Content, expiresIn string) (Result, error) {
	token, tokenHash, err := newToken()
	if err != nil {
		return Result{}, err
	}
	expiresAt, err := expiry(expiresIn)
	if err != nil {
		return Result{}, err
	}
	share, err := s.store.CreateShare(ctx, sqlc.CreateShareParams{
		ID:        uuid.NewString(),
		TokenHash: tokenHash,
		UserID:    userID,
		Title:     c.Title,
		MediaType: c.MediaType,
		Content:   c.Data,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		ID:        share.ID,
		Token:     token,
		Title:     share.Title,
		MediaType: share.MediaType,
		ExpiresAt: share.ExpiresAt,
		CreatedAt: share.CreatedAt,
	}, nil
}

// SafePath resolves a caller-supplied relative path against root and guarantees
// the result stays within root (directory-traversal guard).
func SafePath(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace root")
	}
	return abs, nil
}

// MediaType maps a path's extension to the media type used for the share, or ""
// if the type is not allowed to be shared.
func MediaType(path string) string {
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

func expiry(preset string) (sql.NullString, error) {
	value := preset
	if value == "" {
		value = "7d"
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
		return sql.NullString{}, ErrInvalidExpiry
	}
	return sql.NullString{String: time.Now().UTC().Add(d).Format("2006-01-02 15:04:05"), Valid: true}, nil
}

func newToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, TokenHash(token), nil
}

// TokenHash returns the stored hash for a share token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
