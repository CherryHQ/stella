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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
	recallySvc *recally.Service
	assets     *asset.Store
	stellaHome string
	baseURL    string
}

// Share is the transport-neutral view of one share row. Content carries the
// stored bytes for a freshly created share and the public token view; it is nil
// for list summaries, whose query does not select the payload. Times are UTC;
// ExpiresAt is nil for a share that never expires. This is the only share shape
// that crosses the package boundary — callers never see sqlc rows or pgtype
// nulls.
type Share struct {
	ID        string
	Title     string
	MediaType string
	Content   []byte
	ExpiresAt *time.Time
	CreatedAt time.Time
}

type Created struct {
	Share Share
	Token string
	URL   string
}

type ListResult struct {
	Shares        []Share
	NextPageToken string
}

// shareFromRow maps the full share row (including content) to the domain value.
func shareFromRow(r sqlc.Share) Share {
	return Share{
		ID:        r.ID,
		Title:     r.Title,
		MediaType: r.MediaType,
		Content:   r.Content,
		ExpiresAt: timePtr(r.ExpiresAt),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

// summaryFromRow maps a list row (no content) to the domain value.
func summaryFromRow(r sqlc.ListSharesByUserRow) Share {
	return Share{
		ID:        r.ID,
		Title:     r.Title,
		MediaType: r.MediaType,
		ExpiresAt: timePtr(r.ExpiresAt),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func timePtr(n pgtype.Timestamptz) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

func NewService(q *sqlc.Queries, mem memory.Provider, store *recally.Store, assets *asset.Store, stellaHome, baseURL string) *Service {
	return &Service{q: q, mem: mem, store: store, recallySvc: recally.NewService(store, stellaHome), assets: assets, stellaHome: stellaHome, baseURL: strings.TrimRight(baseURL, "/")}
}

// NewServiceForPool creates a share service that owns the sqlc query set for the
// share tables, so callers pass only the pgx pool.
func NewServiceForPool(pool *pgxpool.Pool, mem memory.Provider, store *recally.Store, assets *asset.Store, stellaHome, baseURL string) *Service {
	return NewService(sqlc.New(pool), mem, store, assets, stellaHome, baseURL)
}

func (s *Service) PublicURL(token string) string {
	if token == "" || s.baseURL == "" {
		return ""
	}
	return s.baseURL + "/s/" + url.PathEscape(token)
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
	return Created{Share: shareFromRow(row), Token: token, URL: s.PublicURL(token)}, nil
}

// PublicContent resolves a public share by its capability token. It hashes the
// token, looks the row up by hash, and returns the share with its content. This
// is the capability-URL view served without a session (see Access), so it does
// not authorize against a user; possession of the unguessable token is the
// grant. An unknown token (or any lookup failure) is authz.ErrNotFound, so the
// transport surfaces a uniform 404 and never distinguishes missing from expired.
func (s *Service) PublicContent(ctx context.Context, token string) (Share, error) {
	if s == nil || token == "" {
		return Share{}, authz.ErrNotFound
	}
	row, err := s.q.GetShareByTokenHash(ctx, TokenHash(token))
	if err != nil {
		return Share{}, authz.ErrNotFound
	}
	return shareFromRow(row), nil
}

func (s *Service) sessionWorkspaceRoot(ctx context.Context, ident authz.Identity, sessionID, scope string) (string, error) {
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		return "", authz.ErrNotFound
	}
	loadCtx := authz.WithUserID(ctx, ident.UserID)
	if ident.AgentID != "" {
		loadCtx = authz.WithAgentID(loadCtx, ident.AgentID)
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
	name, err := safePathName(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// OpenSafeRoot returns an os.Root and root-relative name. Security-sensitive
// operations use Root methods so symlink swaps cannot escape the workspace.
func OpenSafeRoot(root, rel string) (*os.Root, string, error) {
	name, err := safePathName(rel)
	if err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	return r, name, nil
}

func safePathName(rel string) (string, error) {
	name := filepath.Clean(filepath.FromSlash(rel))
	if !filepath.IsLocal(name) {
		return "", ErrPathEscapes
	}
	return name, nil
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
