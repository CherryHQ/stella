package recally

import (
	"context"
	"errors"
	"fmt"
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

func (s Authorized) GetFeed(ctx context.Context, feedID string) (*Feed, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	feed, err := s.store.GetFeed(ctx, uid, feedID)
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

func (s Authorized) PollFeed(ctx context.Context, id string, limit int) (FeedPollResult, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return FeedPollResult{}, err
	}
	feed, err := s.store.GetFeed(ctx, uid, id)
	if err != nil {
		return FeedPollResult{}, mapMissing(err)
	}
	return s.pollFeed(ctx, uid, *feed, limit), nil
}

func (s Authorized) PollFeeds(ctx context.Context, limit int) ([]FeedPollResult, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	const pageSize = 500
	results := []FeedPollResult{}
	for offset := 0; ; offset += pageSize {
		feeds, err := s.store.ListFeeds(ctx, uid, pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, feed := range feeds {
			if !feed.Enabled || feed.Kind != FeedKindRSS {
				continue
			}
			results = append(results, s.pollFeed(ctx, uid, feed, limit))
		}
		if len(feeds) < pageSize {
			break
		}
	}
	return results, nil
}

func (s Authorized) pollFeed(ctx context.Context, uid string, feed Feed, limit int) FeedPollResult {
	result := FeedPollResult{Feed: feed, NewEntries: []FeedEntry{}}
	if !feed.Enabled || feed.Kind != FeedKindRSS {
		return result
	}

	parseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	parsed, parseErr := s.feeds.ParseURLWithContext(feed.URL, parseCtx)
	cancel()
	if parseErr != nil {
		result.Errors = []string{"failed to fetch feed"}
		return result
	}
	for _, item := range parsed.Items {
		entryURL := item.Link
		if entryURL == "" && item.GUID != "" {
			entryURL = item.GUID
		}
		guid := item.GUID
		if guid == "" {
			guid = entryURL
		}
		entry, createErr := s.store.CreateFeedEntry(ctx, feed.ID, guid, entryURL, item.Title)
		if createErr != nil || entry == nil {
			continue
		}
		result.NewEntries = append(result.NewEntries, *entry)
		if len(result.NewEntries) >= limit {
			break
		}
	}

	now := time.Now().UTC()
	if updated, err := s.store.UpdateFeed(ctx, uid, feed.ID, map[string]any{"last_checked_at": &now}); err == nil {
		result.Feed = *updated
	}
	return result
}

func (s Authorized) ListFeedEntries(ctx context.Context, feedID string, filter FeedEntryFilter) ([]FeedEntry, error) {
	if _, err := s.GetFeed(ctx, feedID); err != nil {
		return nil, err
	}
	status := filter.Status
	if status == "" {
		status = EntryStatusPending
	}
	if status != EntryStatusPending {
		return nil, fmt.Errorf("only status=pending is supported")
	}
	return s.store.ListPendingFeedEntries(ctx, feedID, filter.Limit, filter.Offset)
}

func (s Authorized) CreateFeedEntry(ctx context.Context, feedID, guid, entryURL, title string) (*FeedEntry, bool, error) {
	if _, err := s.GetFeed(ctx, feedID); err != nil {
		return nil, false, err
	}
	if guid == "" {
		return nil, false, fmt.Errorf("guid is required")
	}
	entry, err := s.store.CreateFeedEntry(ctx, feedID, guid, entryURL, title)
	if err != nil {
		return nil, false, err
	}
	return entry, entry != nil, nil
}

func (s Authorized) UpdateFeedEntry(ctx context.Context, feedID, id string, status RSSEntryStatus, articleID *string, errorMsg string) (*FeedEntry, error) {
	if _, err := s.GetFeed(ctx, feedID); err != nil {
		return nil, err
	}
	entry, err := s.store.GetFeedEntry(ctx, feedID, id)
	if err != nil || entry.FeedID != feedID {
		return nil, authz.ErrNotFound
	}
	switch status {
	case EntryStatusSaved, EntryStatusSkipped, EntryStatusError, EntryStatusPending:
	default:
		return nil, fmt.Errorf("invalid status")
	}
	if status == EntryStatusSaved && (articleID == nil || *articleID == "") {
		return nil, fmt.Errorf("article_id required when status=saved")
	}
	updated, err := s.store.MarkFeedEntry(ctx, feedID, id, status, articleID, errorMsg)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s Authorized) GetDigest(ctx context.Context) (*Digest, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	return s.store.GetDigest(ctx, uid)
}

func (s Authorized) SaveDigest(ctx context.Context, narrative, date string) (*StoredDigest, error) {
	ident := s.ident
	uid, err := userID(ident)
	if err != nil {
		return nil, err
	}
	if narrative == "" {
		return nil, fmt.Errorf("narrative is required")
	}
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	return s.store.SaveDigest(ctx, uid, narrative, date)
}
