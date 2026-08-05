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
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const MaxShareSize = 25 * 1024 * 1024

var (
	ErrTooLarge            = errors.New("file is too large to share")
	ErrUnsupportedType     = errors.New("unsupported artifact type")
	ErrDirectory           = errors.New("path is a directory")
	ErrNoContent           = errors.New("article has no content")
	ErrInvalidInput        = errors.New("invalid input")
	ErrInvalidArtifactPath = fmt.Errorf("invalid artifact path: %w", ErrInvalidInput)
)

// Service creates immutable PostgreSQL snapshots. Artifact bytes come only from
// session access; article bytes come only from Recally's owner-scoped Access.
type Service struct {
	q        *sqlc.Queries
	sessions *sessionaccess.Service
	recally  *recally.Service
	baseURL  string
}

// Share is the transport-neutral view of one share row. Content carries the
// stored bytes for a freshly created share and the public token view; it is nil
// for list summaries, whose query does not select the payload. Times are UTC;
// ExpiresAt is nil for a share that never expires.
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

func shareFromRow(r sqlc.Share) Share {
	return Share{ID: r.ID, Title: r.Title, MediaType: r.MediaType, Content: r.Content, ExpiresAt: timePtr(r.ExpiresAt), CreatedAt: r.CreatedAt.UTC()}
}

func summaryFromRow(r sqlc.ListSharesByUserRow) Share {
	return Share{ID: r.ID, Title: r.Title, MediaType: r.MediaType, ExpiresAt: timePtr(r.ExpiresAt), CreatedAt: r.CreatedAt.UTC()}
}

func timePtr(n pgtype.Timestamptz) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

func NewService(q *sqlc.Queries, sessions *sessionaccess.Service, recallySvc *recally.Service, baseURL string) *Service {
	return &Service{q: q, sessions: sessions, recally: recallySvc, baseURL: strings.TrimRight(baseURL, "/")}
}

// NewServiceForPool creates a share service that owns the sqlc query set for
// share tables, while source reads stay behind their application services.
func NewServiceForPool(pool *pgxpool.Pool, sessions *sessionaccess.Service, recallySvc *recally.Service, baseURL string) *Service {
	return NewService(sqlc.New(pool), sessions, recallySvc, baseURL)
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

// PublicContent resolves a public share by its capability token. An unknown
// token (or any lookup failure) is authz.ErrNotFound to preserve uniform 404s.
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

func ArtifactMediaType(name string) string {
	ext := strings.ToLower(path.Ext(name))
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
