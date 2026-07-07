package recally

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/CherryHQ/stella/internal/authz"
)

// Authorized is the identity-scoped view of the service; all authorization
// checks live on its methods.
type Authorized struct {
	*Service
	ident authz.Identity
}

func (s *Service) As(ident authz.Identity) Authorized { return Authorized{Service: s, ident: ident} }

type Service struct {
	store      *Store
	files      *FileManager
	feeds      *gofeed.Parser
	stellaHome string
}

type SaveResult struct {
	Article *Article
	Created bool
}

func NewService(store *Store, files *FileManager, stellaHome string) *Service {
	return &Service{store: store, files: files, feeds: gofeed.NewParser(), stellaHome: stellaHome}
}

func (s *Service) Store() *Store { return s.store }

func (s *Service) Files() *FileManager { return s.files }

// Recally is deliberately user-owned, not agent-scoped: a user's reading
// library is shared across all of their agents.
func userID(ident authz.Identity) (string, error) {
	if err := ident.RequireUser(); err != nil {
		return "", err
	}
	return ident.UserID, nil
}

func mapMissing(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return authz.ErrNotFound
	}
	return err
}

func (s Authorized) Save(ctx context.Context, req SaveRequest) (SaveResult, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return SaveResult{}, err
	}
	article, created, err := s.saveWithFile(ctx, uid, req)
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Article: article, Created: created}, nil
}

func (s *Service) saveWithFile(ctx context.Context, userID string, req SaveRequest) (*Article, bool, error) {
	if req.URL == "" {
		return nil, false, fmt.Errorf("url is required")
	}
	canonical := req.CanonicalURL
	if canonical == "" {
		canonical = NormalizeURL(req.URL)
		req.CanonicalURL = canonical
	}
	existing, lookupErr := s.store.GetArticleByCanonicalURL(ctx, userID, canonical)
	content := req.Content
	if content == "" && lookupErr != nil {
		return nil, false, fmt.Errorf("content is required for new articles")
	}
	var filePath string
	if existing != nil {
		if existing.FilePath != "" {
			filePath = existing.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(s.stellaHome, filePath)
			}
		}
		if content == "" {
			if body, ok, err := s.store.GetArticleContent(ctx, existing.ID); err != nil {
				return nil, false, err
			} else if ok {
				content = body
			}
		}
		if content == "" && filePath != "" {
			if body, readErr := s.files.ReadArticle(filePath); readErr == nil {
				content = body
			}
		}
	}
	article, isNew, err := s.store.SaveArticleWithContent(ctx, userID, req, content)
	if err != nil {
		return nil, false, err
	}
	if filePath == "" {
		filePath = s.files.ArticlePath(userID, article.ID, article.Title, article.SavedAt)
	}
	if !isNew && content != "" {
		if err := s.store.UpsertArticleContent(ctx, article.ID, content); err != nil {
			return nil, false, err
		}
	}
	if content == "" {
		return article, isNew, nil
	}
	if err := s.files.WriteArticle(filePath, article, content); err != nil {
		slog.Warn("failed to mirror recally article body to disk", "article_id", article.ID, "path", filePath, "error", err)
		return article, isNew, nil
	}
	relPath := s.files.RelativePath(filePath)
	if err := s.store.UpdateArticleFilePath(ctx, userID, article.ID, relPath); err != nil {
		slog.Warn("failed to update recally article mirror path", "article_id", article.ID, "path", relPath, "error", err)
		return article, isNew, nil
	}
	article.FilePath = relPath
	return article, isNew, nil
}

func (s Authorized) GetArticle(ctx context.Context, id string) (*Article, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	article, err := s.store.GetArticle(ctx, uid, id)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (s *Service) ReadArticleBody(ctx context.Context, article *Article) (string, error) {
	if article == nil {
		return "", nil
	}
	if body, ok, err := s.store.GetArticleContent(ctx, article.ID); err != nil {
		return "", err
	} else if ok && body != "" {
		return body, nil
	}
	if article.FilePath == "" {
		return "", nil
	}
	path := article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.stellaHome, path)
	}
	body, err := s.files.ReadArticle(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if body != "" {
		if err := s.store.InsertArticleContentIfAbsent(ctx, article.ID, body); err != nil {
			slog.Warn("failed to backfill recally article content from disk", "article_id", article.ID, "error", err)
		}
	}
	return body, nil
}

// BackfillMissingContent copies legacy article bodies that live only in a disk
// mirror into recally_article_content. Lazy backfill on read (ReadArticleBody) is
// not enough on k8s, where the pod-local disk that holds the mirror disappears on
// reschedule; this runs at startup to durably capture whatever the current pod can
// still read. A missing or unreadable file is skipped (it may belong to another
// pod's disk), never fatal. Returns scanned/backfilled/missing counts.
func (s *Service) BackfillMissingContent(ctx context.Context) (scanned, backfilled, missing int, err error) {
	rows, err := s.store.ListArticlesMissingContent(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return scanned, backfilled, missing, err
		}
		scanned++
		if row.FilePath == "" {
			missing++
			continue
		}
		path := row.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.stellaHome, path)
		}
		body, readErr := s.files.ReadArticle(path)
		if readErr != nil || body == "" {
			missing++
			continue
		}
		if err := s.store.InsertArticleContentIfAbsent(ctx, row.ID, body); err != nil {
			slog.Warn("failed to backfill recally article content from disk", "article_id", row.ID, "error", err)
			continue
		}
		backfilled++
	}
	return scanned, backfilled, missing, nil
}

func (s Authorized) ListArticles(ctx context.Context, filter ArticleFilter) ([]Article, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.ListArticles(ctx, uid, filter)
}

func (s Authorized) SearchArticles(ctx context.Context, query string, limit int) ([]Article, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.SearchArticles(ctx, uid, query, limit)
}

func (s Authorized) GetArticleByCanonicalURL(ctx context.Context, canonicalURL string) (*Article, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	article, err := s.store.GetArticleByCanonicalURL(ctx, uid, canonicalURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (s Authorized) ListFeeds(ctx context.Context, limit, offset int) ([]Feed, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.ListFeeds(ctx, uid, limit, offset)
}

func (s Authorized) GetFeedByURL(ctx context.Context, feedURL string) (*Feed, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	feed, err := s.store.GetFeedByURL(ctx, uid, feedURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return feed, nil
}

func (s Authorized) CreateFeed(ctx context.Context, feedURL string, kind FeedKind, title string, agentID *string) (*Feed, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	if feedURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if existing, _ := s.store.GetFeedByURL(ctx, uid, feedURL); existing != nil {
		return nil, fmt.Errorf("feed already subscribed")
	}
	if kind == "" {
		kind = SniffFeedKind(feedURL)
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("invalid feed kind")
	}
	if err := ValidateFeedSubscription(feedURL, kind); err != nil {
		return nil, err
	}
	description := ""
	if kind == FeedKindRSS {
		parseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		parsed, parseErr := s.feeds.ParseURLWithContext(feedURL, parseCtx)
		cancel()
		if parseErr != nil {
			return nil, parseErr
		}
		if title == "" {
			title = parsed.Title
		}
		description = parsed.Description
	}
	return s.store.CreateFeed(ctx, uid, feedURL, kind, nil, title, description, agentID)
}

func (s Authorized) DeleteFeed(ctx context.Context, id string) error {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return err
	}
	if _, err := s.store.GetFeed(ctx, uid, id); err != nil {
		return mapMissing(err)
	}
	if err := s.store.DeleteFeed(ctx, uid, id); err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return authz.ErrNotFound
		}
		return err
	}
	return nil
}

func (s Authorized) GetDigest(ctx context.Context) (*Digest, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.GetDigest(ctx, uid)
}
