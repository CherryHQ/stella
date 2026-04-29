// Package recally provides database operations for articles and feeds.
package recally

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/vaayne/anna/pkg/db/sqlc"
)

// Store provides database operations for the recally package.
type Store struct {
	db *sql.DB
	q  *sqlc.Queries
}

// NewStore creates a new Store instance.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, q: sqlc.New(db)}
}

func generateID() string {
	return ulid.Make().String()
}

// SaveArticle saves or updates an article by canonical URL.
func (s *Store) SaveArticle(ctx context.Context, userID int64, req SaveRequest) (*Article, bool, error) {
	canonicalURL := req.CanonicalURL
	if canonicalURL == "" {
		canonicalURL = NormalizeURL(req.URL)
	}

	existing, err := s.q.GetArticleByCanonicalURL(ctx, sqlc.GetArticleByCanonicalURLParams{UserID: userID, CanonicalUrl: canonicalURL})
	switch {
	case err == nil:
		updated, err := s.q.UpdateArticle(ctx, sqlc.UpdateArticleParams{
			ID:          existing.ID,
			Title:       req.Title,
			Author:      req.Author,
			Summary:     req.Summary,
			Tags:        encodeTags(req.Tags),
			Status:      existing.Status,
			Starred:     existing.Starred,
			FilePath:    existing.FilePath,
			Metadata:    encodeMetadata(req.Metadata),
			PublishedAt: toNullTime(req.PublishedAt),
			ReadAt:      existing.ReadAt,
		})
		if err != nil {
			return nil, false, fmt.Errorf("update article: %w", err)
		}
		var article Article
		article.FromSQLCArticle(updated)
		article.IsNew = false
		return &article, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, false, fmt.Errorf("lookup article by canonical url: %w", err)
	}

	created, err := s.q.CreateArticle(ctx, sqlc.CreateArticleParams{
		ID:           generateID(),
		UserID:       userID,
		AgentID:      toNullString(req.AgentID),
		Url:          req.URL,
		CanonicalUrl: canonicalURL,
		SourceType:   string(req.SourceType),
		Title:        req.Title,
		Author:       req.Author,
		Summary:      req.Summary,
		Tags:         encodeTags(req.Tags),
		Status:       string(StatusUnread),
		Starred:      0,
		FilePath:     "",
		Metadata:     encodeMetadata(req.Metadata),
		PublishedAt:  toNullTime(req.PublishedAt),
		SavedAt:      time.Now().UTC().Format(time.RFC3339),
		ReadAt:       sql.NullString{},
	})
	if err != nil {
		return nil, false, fmt.Errorf("create article: %w", err)
	}

	var article Article
	article.FromSQLCArticle(created)
	article.IsNew = true
	return &article, true, nil
}

// GetArticle retrieves an article by ID.
func (s *Store) GetArticle(ctx context.Context, articleID string) (*Article, error) {
	row, err := s.q.GetArticle(ctx, articleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("article not found: %s", articleID)
		}
		return nil, fmt.Errorf("get article: %w", err)
	}
	var article Article
	article.FromSQLCArticle(row)
	return &article, nil
}

// UpdateArticle updates article metadata.
func (s *Store) UpdateArticle(ctx context.Context, articleID string, updates map[string]any) (*Article, error) {
	current, err := s.q.GetArticle(ctx, articleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("article not found: %s", articleID)
		}
		return nil, fmt.Errorf("get article for update: %w", err)
	}

	title := current.Title
	author := current.Author
	summary := current.Summary
	tags := current.Tags
	status := current.Status
	starred := current.Starred
	filePath := current.FilePath
	metadata := current.Metadata
	publishedAt := current.PublishedAt
	readAt := current.ReadAt

	if v, ok := updates["title"].(string); ok {
		title = v
	}
	if v, ok := updates["author"].(string); ok {
		author = v
	}
	if v, ok := updates["summary"].(string); ok {
		summary = v
	}
	if v, ok := updates["tags"].([]string); ok {
		tags = encodeTags(v)
	}
	if v, ok := updates["status"].(string); ok {
		status = v
		if v == string(StatusRead) && current.Status != string(StatusRead) {
			readAt = toNullTime(ptrTime(time.Now().UTC()))
		}
	}
	if v, ok := updates["starred"].(bool); ok {
		starred = boolToInt64(v)
	}
	if v, ok := updates["file_path"].(string); ok {
		filePath = v
	}
	if v, ok := updates["metadata"].(map[string]string); ok {
		metadata = encodeMetadata(v)
	}
	if v, ok := updates["published_at"].(*time.Time); ok {
		publishedAt = toNullTime(v)
	}

	updated, err := s.q.UpdateArticle(ctx, sqlc.UpdateArticleParams{
		ID:          articleID,
		Title:       title,
		Author:      author,
		Summary:     summary,
		Tags:        tags,
		Status:      status,
		Starred:     starred,
		FilePath:    filePath,
		Metadata:    metadata,
		PublishedAt: publishedAt,
		ReadAt:      readAt,
	})
	if err != nil {
		return nil, fmt.Errorf("update article: %w", err)
	}

	var article Article
	article.FromSQLCArticle(updated)
	return &article, nil
}

// DeleteArticle removes an article from the database.
func (s *Store) DeleteArticle(ctx context.Context, articleID string) error {
	if err := s.q.DeleteArticle(ctx, articleID); err != nil {
		return fmt.Errorf("delete article: %w", err)
	}
	return nil
}

// ListArticles lists articles for a user with optional filtering.
func (s *Store) ListArticles(ctx context.Context, userID int64, filter ArticleFilter) ([]Article, error) {
	limit := int64(filter.Limit)
	if limit <= 0 {
		limit = 50
	}

	starred := any(int64(0))
	if filter.Starred != nil && *filter.Starred {
		starred = int64(1)
	}

	rows, err := s.q.ListArticles(ctx, sqlc.ListArticlesParams{
		UserID:     userID,
		Status:     emptyOrString(string(filter.Status)),
		SourceType: emptyOrString(string(filter.SourceType)),
		Starred:    starred,
		Limit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}

	articles := make([]Article, 0, len(rows))
	for _, row := range rows {
		var article Article
		article.FromSQLCArticle(row)
		articles = append(articles, article)
	}
	return articles, nil
}

// SearchArticles searches articles by title, summary, tags, or author.
func (s *Store) SearchArticles(ctx context.Context, userID int64, query string, limit int) ([]Article, error) {
	if limit <= 0 {
		limit = 50
	}
	needle := sql.NullString{String: query, Valid: query != ""}
	rows, err := s.q.SearchArticles(ctx, sqlc.SearchArticlesParams{
		UserID:  userID,
		Column2: needle,
		Column3: needle,
		Column4: needle,
		Column5: needle,
		Limit:   int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search articles: %w", err)
	}

	articles := make([]Article, 0, len(rows))
	for _, row := range rows {
		var article Article
		article.FromSQLCArticle(row)
		articles = append(articles, article)
	}
	return articles, nil
}

// CreateFeed creates a new RSS feed subscription.
func (s *Store) CreateFeed(ctx context.Context, userID int64, feedURL, title, description string, agentID *string) (*Feed, error) {
	row, err := s.q.CreateRSSFeed(ctx, sqlc.CreateRSSFeedParams{
		ID:            generateID(),
		UserID:        userID,
		AgentID:       toNullString(agentID),
		Url:           feedURL,
		Title:         title,
		Description:   description,
		CheckInterval: "1h",
		LastCheckedAt: sql.NullString{},
		LastEtag:      "",
		LastModified:  "",
		Enabled:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("create feed: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// GetFeed retrieves a feed by ID.
func (s *Store) GetFeed(ctx context.Context, feedID string) (*Feed, error) {
	row, err := s.q.GetRSSFeed(ctx, feedID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("feed not found: %s", feedID)
		}
		return nil, fmt.Errorf("get feed: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// GetFeedByURL retrieves a feed by URL for a user.
func (s *Store) GetFeedByURL(ctx context.Context, userID int64, feedURL string) (*Feed, error) {
	row, err := s.q.GetRSSFeedByURL(ctx, sqlc.GetRSSFeedByURLParams{UserID: userID, Url: feedURL})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("feed not found")
		}
		return nil, fmt.Errorf("get feed by url: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// ListFeeds lists all feeds for a user.
func (s *Store) ListFeeds(ctx context.Context, userID int64) ([]Feed, error) {
	rows, err := s.q.ListRSSFeeds(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list feeds: %w", err)
	}
	feeds := make([]Feed, 0, len(rows))
	for _, row := range rows {
		var feed Feed
		feed.FromSQLCFeed(row)
		feeds = append(feeds, feed)
	}
	return feeds, nil
}

// UpdateFeed updates feed metadata.
func (s *Store) UpdateFeed(ctx context.Context, feedID string, updates map[string]any) (*Feed, error) {
	current, err := s.q.GetRSSFeed(ctx, feedID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("feed not found: %s", feedID)
		}
		return nil, fmt.Errorf("get feed for update: %w", err)
	}

	title := current.Title
	description := current.Description
	checkInterval := current.CheckInterval
	lastCheckedAt := current.LastCheckedAt
	lastETag := current.LastEtag
	lastModified := current.LastModified
	enabled := current.Enabled

	if v, ok := updates["title"].(string); ok {
		title = v
	}
	if v, ok := updates["description"].(string); ok {
		description = v
	}
	if v, ok := updates["check_interval"].(string); ok {
		checkInterval = v
	}
	if v, ok := updates["last_checked_at"].(*time.Time); ok {
		lastCheckedAt = toNullTime(v)
	}
	if v, ok := updates["last_etag"].(string); ok {
		lastETag = v
	}
	if v, ok := updates["last_modified"].(string); ok {
		lastModified = v
	}
	if v, ok := updates["enabled"].(bool); ok {
		enabled = boolToInt64(v)
	}

	updated, err := s.q.UpdateRSSFeed(ctx, sqlc.UpdateRSSFeedParams{
		ID:            feedID,
		Title:         title,
		Description:   description,
		CheckInterval: checkInterval,
		LastCheckedAt: lastCheckedAt,
		LastEtag:      lastETag,
		LastModified:  lastModified,
		Enabled:       enabled,
	})
	if err != nil {
		return nil, fmt.Errorf("update feed: %w", err)
	}

	var feed Feed
	feed.FromSQLCFeed(updated)
	return &feed, nil
}

// DeleteFeed removes a feed and all its entries.
func (s *Store) DeleteFeed(ctx context.Context, feedID string) error {
	if err := s.q.DeleteRSSFeed(ctx, feedID); err != nil {
		return fmt.Errorf("delete feed: %w", err)
	}
	return nil
}

// CreateFeedEntry creates a new feed entry.
func (s *Store) CreateFeedEntry(ctx context.Context, feedID, guid, entryURL, title string) (*FeedEntry, error) {
	row, err := s.q.CreateRSSFeedEntry(ctx, sqlc.CreateRSSFeedEntryParams{
		ID:           generateID(),
		FeedID:       feedID,
		Guid:         guid,
		Url:          entryURL,
		Title:        title,
		Status:       string(EntryStatusPending),
		ArticleID:    sql.NullString{},
		Attempts:     0,
		ErrorMsg:     "",
		DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
		ProcessedAt:  sql.NullString{},
	})
	if err != nil {
		return nil, fmt.Errorf("create feed entry: %w", err)
	}
	var entry FeedEntry
	entry.FromSQLCFeedEntry(row)
	return &entry, nil
}

// ListPendingFeedEntries lists pending entries for processing.
func (s *Store) ListPendingFeedEntries(ctx context.Context, feedID string, limit int) ([]FeedEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListPendingRSSEntries(ctx, sqlc.ListPendingRSSEntriesParams{FeedID: feedID, Limit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("list pending entries: %w", err)
	}
	entries := make([]FeedEntry, 0, len(rows))
	for _, row := range rows {
		var entry FeedEntry
		entry.FromSQLCFeedEntry(row)
		entries = append(entries, entry)
	}
	return entries, nil
}

// MarkFeedEntry updates the status of a feed entry after processing.
func (s *Store) MarkFeedEntry(ctx context.Context, entryID string, status RSSEntryStatus, articleID *string, errorMsg string) (*FeedEntry, error) {
	updated, err := s.q.UpdateRSSFeedEntry(ctx, sqlc.UpdateRSSFeedEntryParams{
		ID:        entryID,
		Status:    string(status),
		ArticleID: toNullString(articleID),
		ErrorMsg:  errorMsg,
	})
	if err != nil {
		return nil, fmt.Errorf("update feed entry: %w", err)
	}
	var entry FeedEntry
	entry.FromSQLCFeedEntry(updated)
	return &entry, nil
}

// UpdateArticleFilePath updates only the file path of an article.
func (s *Store) UpdateArticleFilePath(ctx context.Context, articleID, filePath string) error {
	_, err := s.UpdateArticle(ctx, articleID, map[string]any{"file_path": filePath})
	if err != nil {
		return fmt.Errorf("update file path: %w", err)
	}
	return nil
}

// GetDigest generates a daily reading digest for a user.
func (s *Store) GetDigest(ctx context.Context, userID int64) (*Digest, error) {
	digest := &Digest{
		UserID: userID,
		Date:   time.Now().UTC(),
	}

	// Count by status
	unreadCount, err := s.q.CountArticlesByStatus(ctx, sqlc.CountArticlesByStatusParams{
		UserID: userID,
		Status: string(StatusUnread),
	})
	if err != nil {
		return nil, fmt.Errorf("count unread articles: %w", err)
	}
	digest.UnreadCount = unreadCount

	readCount, err := s.q.CountArticlesByStatus(ctx, sqlc.CountArticlesByStatusParams{
		UserID: userID,
		Status: string(StatusRead),
	})
	if err != nil {
		return nil, fmt.Errorf("count read articles: %w", err)
	}
	digest.ReadCount = readCount

	archivedCount, err := s.q.CountArticlesByStatus(ctx, sqlc.CountArticlesByStatusParams{
		UserID: userID,
		Status: string(StatusArchived),
	})
	if err != nil {
		return nil, fmt.Errorf("count archived articles: %w", err)
	}
	digest.ArchivedCount = archivedCount

	digest.TotalArticles = unreadCount + readCount + archivedCount

	// Count starred
	starredCount, err := s.q.CountStarredArticles(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("count starred articles: %w", err)
	}
	digest.StarredCount = starredCount

	// Articles saved yesterday
	yesterdayRows, err := s.q.ListArticlesSavedYesterday(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list yesterday's articles: %w", err)
	}
	digest.SavedYesterday = make([]Article, 0, len(yesterdayRows))
	for _, row := range yesterdayRows {
		var article Article
		article.FromSQLCArticle(row)
		digest.SavedYesterday = append(digest.SavedYesterday, article)
	}
	digest.SavedYesterdayCount = len(digest.SavedYesterday)

	// Articles worth revisiting (unread and older than 3 days)
	revisitRows, err := s.q.ListUnreadArticlesOlderThan(ctx, sqlc.ListUnreadArticlesOlderThanParams{
		UserID:   userID,
		Datetime: "-3 days",
		Limit:    10,
	})
	if err != nil {
		return nil, fmt.Errorf("list revisiting articles: %w", err)
	}
	digest.WorthRevisiting = make([]Article, 0, len(revisitRows))
	for _, row := range revisitRows {
		var article Article
		article.FromSQLCArticle(row)
		digest.WorthRevisiting = append(digest.WorthRevisiting, article)
	}
	digest.WorthRevisitingCount = len(digest.WorthRevisiting)

	// Top tags this week - count from articles saved this week
	weekRows, err := s.q.GetArticlesSavedThisWeek(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get articles this week: %w", err)
	}

	// Count tag frequencies
	tagFreq := make(map[string]int64)
	for _, row := range weekRows {
		tags := decodeTags(row.Tags)
		for _, tag := range tags {
			tagFreq[tag]++
		}
	}

	// Convert to sorted slice
	digest.TopTags = make([]TagCount, 0, len(tagFreq))
	for tag, count := range tagFreq {
		digest.TopTags = append(digest.TopTags, TagCount{
			Tag:   tag,
			Count: count,
		})
	}
	// Sort by count descending (simple bubble sort for small lists)
	for i := 0; i < len(digest.TopTags); i++ {
		for j := i + 1; j < len(digest.TopTags); j++ {
			if digest.TopTags[j].Count > digest.TopTags[i].Count {
				digest.TopTags[i], digest.TopTags[j] = digest.TopTags[j], digest.TopTags[i]
			}
		}
	}
	// Limit to top 10
	if len(digest.TopTags) > 10 {
		digest.TopTags = digest.TopTags[:10]
	}

	return digest, nil
}

func emptyOrString(value string) any {
	if value == "" {
		return ""
	}
	return value
}

func toNullString(value *string) sql.NullString {
	if value == nil || *value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func toNullTime(value *time.Time) sql.NullString {
	if value == nil || value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: value.UTC().Format(time.RFC3339), Valid: true}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
