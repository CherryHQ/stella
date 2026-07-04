package recally

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/CherryHQ/stella/internal/toolctx"
)

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
func userID(ident toolctx.Identity) (string, error) {
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
		return toolctx.ErrNotFound
	}
	return err
}

func (s *Service) SaveOwned(ctx context.Context, ident toolctx.Identity, req SaveRequest) (SaveResult, error) {
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
	article, isNew, err := s.store.SaveArticle(ctx, userID, req)
	if err != nil {
		return nil, false, err
	}
	var filePath string
	switch {
	case isNew:
		filePath = s.files.ArticlePath(userID, article.ID, article.Title, article.SavedAt)
	case existing != nil && existing.FilePath != "":
		filePath = existing.FilePath
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(s.stellaHome, filePath)
		}
		if content == "" {
			if body, readErr := s.files.ReadArticle(filePath); readErr == nil {
				content = body
			}
		}
	default:
		filePath = s.files.ArticlePath(userID, article.ID, article.Title, article.SavedAt)
	}
	if err := s.files.WriteArticle(filePath, article, content); err != nil {
		return nil, false, err
	}
	relPath := s.files.RelativePath(filePath)
	if err := s.store.UpdateArticleFilePath(ctx, userID, article.ID, relPath); err != nil {
		return nil, false, err
	}
	article.FilePath = relPath
	return article, isNew, nil
}

func (s *Service) GetArticleOwned(ctx context.Context, ident toolctx.Identity, id string) (*Article, error) {
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

func (s *Service) ReadArticleBody(article *Article) (string, error) {
	if article == nil || article.FilePath == "" {
		return "", nil
	}
	path := article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.stellaHome, path)
	}
	return s.files.ReadArticle(path)
}

func (s *Service) ListArticlesOwned(ctx context.Context, ident toolctx.Identity, filter ArticleFilter) ([]Article, error) {
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.ListArticles(ctx, uid, filter)
}

func (s *Service) SearchArticlesOwned(ctx context.Context, ident toolctx.Identity, query string, limit int) ([]Article, error) {
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.SearchArticles(ctx, uid, query, limit)
}

func (s *Service) GetArticleByCanonicalURLOwned(ctx context.Context, ident toolctx.Identity, canonicalURL string) (*Article, error) {
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

func (s *Service) ListFeedsOwned(ctx context.Context, ident toolctx.Identity, limit, offset int) ([]Feed, error) {
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.ListFeeds(ctx, uid, limit, offset)
}

func (s *Service) GetFeedByURLOwned(ctx context.Context, ident toolctx.Identity, feedURL string) (*Feed, error) {
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

func (s *Service) CreateFeedOwned(ctx context.Context, ident toolctx.Identity, feedURL string, kind FeedKind, title string, agentID *string) (*Feed, error) {
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

func (s *Service) DeleteFeedOwned(ctx context.Context, ident toolctx.Identity, id string) error {
	uid, err := userID(ident)
	if err != nil {
		return err
	}
	if _, err := s.store.GetFeed(ctx, uid, id); err != nil {
		return mapMissing(err)
	}
	if err := s.store.DeleteFeed(ctx, uid, id); err != nil {
		if errors.Is(err, toolctx.ErrNotFound) {
			return toolctx.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *Service) GetDigestOwned(ctx context.Context, ident toolctx.Identity) (*Digest, error) {
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.GetDigest(ctx, uid)
}
