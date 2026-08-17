// Package recally provides database operations for articles and feeds.
package recally

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Store provides database operations for the recally package.
type Store struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

// NewStore creates a new Store instance.
func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db, q: sqlc.New(db)}
}

func generateID() string {
	return ulid.Make().String()
}

// SaveArticle saves or updates an article by canonical URL.
func (s *Store) SaveArticle(ctx context.Context, userID string, req SaveRequest) (*Article, bool, error) {
	return s.saveArticle(ctx, userID, req, "", false)
}

// SaveArticleWithContent saves or updates an article by canonical URL. New
// articles and their body content are inserted in one transaction so a crash
// cannot leave an article row without its source body.
func (s *Store) SaveArticleWithContent(ctx context.Context, userID string, req SaveRequest, content string) (*Article, bool, error) {
	return s.saveArticle(ctx, userID, req, content, true)
}

func (s *Store) saveArticle(ctx context.Context, userID string, req SaveRequest, content string, insertContent bool) (*Article, bool, error) {
	if req.SourceType == "" {
		req.SourceType = SourceTypeWeb
	}
	if !req.SourceType.Valid() {
		return nil, false, fmt.Errorf("save article: invalid source_type %q", req.SourceType)
	}
	canonicalURL := req.CanonicalURL
	if canonicalURL == "" {
		canonicalURL = NormalizeURL(req.URL)
	}

	existing, err := s.q.GetArticleByCanonicalURL(ctx, sqlc.GetArticleByCanonicalURLParams{UserID: userID, CanonicalUrl: canonicalURL})
	switch {
	case err == nil:
		updated, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyArticle, error) {
			return q.UpdateArticle(ctx, sqlc.UpdateArticleParams{
				ID:          existing.ID,
				UserID:      userID,
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
		})
		if err != nil {
			return nil, false, fmt.Errorf("update article: %w", err)
		}
		var article Article
		article.FromSQLCArticle(updated)
		article.IsNew = false
		return &article, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, false, fmt.Errorf("lookup article by canonical url: %w", err)
	}

	params := sqlc.CreateArticleParams{
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
		Starred:      false,
		FilePath:     "",
		Metadata:     encodeMetadata(req.Metadata),
		PublishedAt:  toNullTime(req.PublishedAt),
		SavedAt:      time.Now().UTC(),
		ReadAt:       pgtype.Timestamptz{},
	}

	var created sqlc.RecallyArticle
	if insertContent {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("begin article transaction: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := agentrun.ValidateTx(ctx, tx); err != nil {
			return nil, false, err
		}
		qtx := s.q.WithTx(tx)
		created, err = qtx.CreateArticle(ctx, params)
		if err != nil {
			return nil, false, fmt.Errorf("create article: %w", err)
		}
		if err := qtx.UpsertArticleContent(ctx, sqlc.UpsertArticleContentParams{ArticleID: created.ID, Content: content}); err != nil {
			return nil, false, fmt.Errorf("upsert article content: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit article transaction: %w", err)
		}
	} else {
		created, err = agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyArticle, error) {
			return q.CreateArticle(ctx, params)
		})
		if err != nil {
			return nil, false, fmt.Errorf("create article: %w", err)
		}
	}

	var article Article
	article.FromSQLCArticle(created)
	article.IsNew = true
	return &article, true, nil
}

// GetArticleByCanonicalURL retrieves an article by canonical URL for a user.
func (s *Store) GetArticleByCanonicalURL(ctx context.Context, userID string, canonicalURL string) (*Article, error) {
	row, err := s.q.GetArticleByCanonicalURL(ctx, sqlc.GetArticleByCanonicalURLParams{UserID: userID, CanonicalUrl: canonicalURL})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("article not found for canonical URL: %s", canonicalURL)
		}
		return nil, fmt.Errorf("get article by canonical URL: %w", err)
	}
	var article Article
	article.FromSQLCArticle(row)
	return &article, nil
}

// GetArticle retrieves an article by ID.
func (s *Store) GetArticle(ctx context.Context, userID string, articleID string) (*Article, error) {
	row, err := s.q.GetArticle(ctx, sqlc.GetArticleParams{ID: articleID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("article not found: %s", articleID)
		}
		return nil, fmt.Errorf("get article: %w", err)
	}
	var article Article
	article.FromSQLCArticle(row)
	return &article, nil
}

// UpdateArticle updates article metadata.
func (s *Store) UpdateArticle(ctx context.Context, userID string, articleID string, updates map[string]any) (*Article, error) {
	current, err := s.q.GetArticle(ctx, sqlc.GetArticleParams{ID: articleID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
		if !ArticleStatus(v).Valid() {
			return nil, fmt.Errorf("update article: invalid status %q", v)
		}
		status = v
		if v == string(StatusRead) && current.Status != string(StatusRead) {
			readAt = toNullTime(ptrTime(time.Now().UTC()))
		}
	}
	if v, ok := updates["starred"].(bool); ok {
		starred = v
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

	updated, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyArticle, error) {
		return q.UpdateArticle(ctx, sqlc.UpdateArticleParams{
			ID:          articleID,
			UserID:      userID,
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
	})
	if err != nil {
		return nil, fmt.Errorf("update article: %w", err)
	}

	var article Article
	article.FromSQLCArticle(updated)
	return &article, nil
}

// DeleteArticle removes an article from the database.
func (s *Store) DeleteArticle(ctx context.Context, userID string, articleID string) error {
	if err := agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.DeleteArticle(ctx, sqlc.DeleteArticleParams{ID: articleID, UserID: userID})
	}); err != nil {
		return fmt.Errorf("delete article: %w", err)
	}
	return nil
}

// ListArticles lists articles for a user with optional filtering.
func (s *Store) ListArticles(ctx context.Context, userID string, filter ArticleFilter) ([]Article, error) {
	limit := int64(filter.Limit)
	if limit <= 0 {
		limit = 50
	}

	// SQL: (starred = 0 OR starred = ?), so 0 means "don't filter", 1 means "only starred"
	var starred int64
	if filter.Starred != nil && *filter.Starred {
		starred = int64(1)
	}

	rows, err := s.q.ListArticles(ctx, sqlc.ListArticlesParams{
		UserID:     userID,
		Status:     emptyOrString(string(filter.Status)),
		SourceType: emptyOrString(string(filter.SourceType)),
		Starred:    starred,
		Limit:      int32(limit),
		Offset:     int32(filter.Offset),
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

// SearchArticles searches articles by title, summary, tags, or author using the
// pg_search BM25 index, ordered by relevance (title hits rank highest via native
// paradedb.boost). The raw query goes straight to paradedb.match, which tokenizes
// with the jieba tokenizer (short and CJK queries match) and never errors on
// punctuation, so there is no fallback tier.
func (s *Store) SearchArticles(ctx context.Context, userID string, query string, limit int) ([]Article, error) {
	if limit <= 0 {
		limit = 50
	}
	// normalizeQuery folds punctuation to spaces so jieba never emits a punctuation
	// token that matches unrelated rows; a letter/digit-free query normalizes to ""
	// and short-circuits instead of issuing a no-op query.
	match := normalizeQuery(query)
	if match == "" {
		return []Article{}, nil
	}
	rows, err := s.q.SearchArticles(ctx, sqlc.SearchArticlesParams{
		Match:  match,
		UserID: userID,
		Limit:  int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search articles: %w", err)
	}

	articles := make([]Article, 0, len(rows))
	for _, row := range rows {
		var article Article
		article.FromSQLCArticle(row.RecallyArticle)
		articles = append(articles, article)
	}
	return articles, nil
}

// normalizeQuery folds punctuation and symbols to spaces and collapses runs of
// whitespace, so jieba never emits a punctuation token that matches unrelated
// rows (index-side whitespace stopwords then drop the spaces). A query with no
// letters or digits normalizes to "", which callers treat as a no-op search.
func normalizeQuery(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// CreateFeed creates a new feed subscription. kind is the extraction-dispatch
// hint ("rss", "twitter"); metadata carries source-specific bookkeeping such as
// a stable numeric user id.
func (s *Store) CreateFeed(ctx context.Context, userID string, feedURL string, kind FeedKind, metadata map[string]string, title, description string, agentID *string) (*Feed, error) {
	if kind == "" {
		kind = FeedKindRSS
	}
	row, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyFeed, error) {
		return q.CreateFeed(ctx, sqlc.CreateFeedParams{
			ID:            generateID(),
			UserID:        userID,
			AgentID:       toNullString(agentID),
			Url:           feedURL,
			Kind:          string(kind),
			Metadata:      encodeMetadata(metadata),
			Title:         title,
			Description:   description,
			CheckInterval: "1h",
			LastCheckedAt: pgtype.Timestamptz{},
			LastEtag:      "",
			LastModified:  "",
			Enabled:       true,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create feed: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// GetFeed retrieves a feed by ID.
func (s *Store) GetFeed(ctx context.Context, userID string, feedID string) (*Feed, error) {
	row, err := s.q.GetFeed(ctx, sqlc.GetFeedParams{ID: feedID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("feed not found: %s", feedID)
		}
		return nil, fmt.Errorf("get feed: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// GetFeedByURL retrieves a feed by URL for a user.
func (s *Store) GetFeedByURL(ctx context.Context, userID string, feedURL string) (*Feed, error) {
	row, err := s.q.GetFeedByURL(ctx, sqlc.GetFeedByURLParams{UserID: userID, Url: feedURL})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("feed not found")
		}
		return nil, fmt.Errorf("get feed by url: %w", err)
	}
	var feed Feed
	feed.FromSQLCFeed(row)
	return &feed, nil
}

// ListFeeds lists all feeds for a user.
func (s *Store) ListFeeds(ctx context.Context, userID string, limit, offset int) ([]Feed, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListFeeds(ctx, sqlc.ListFeedsParams{
		UserID: userID,
		Limit:  int32(limit),
		Offset: int32(offset),
	})
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
func (s *Store) UpdateFeed(ctx context.Context, userID string, feedID string, updates map[string]any) (*Feed, error) {
	current, err := s.q.GetFeed(ctx, sqlc.GetFeedParams{ID: feedID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("feed not found: %s", feedID)
		}
		return nil, fmt.Errorf("get feed for update: %w", err)
	}

	title := current.Title
	description := current.Description
	metadata := current.Metadata
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
	if v, ok := updates["metadata"].(map[string]string); ok {
		metadata = encodeMetadata(v)
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
		enabled = v
	}

	updated, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyFeed, error) {
		return q.UpdateFeed(ctx, sqlc.UpdateFeedParams{
			ID:            feedID,
			UserID:        userID,
			Title:         title,
			Description:   description,
			Metadata:      metadata,
			CheckInterval: checkInterval,
			LastCheckedAt: lastCheckedAt,
			LastEtag:      lastETag,
			LastModified:  lastModified,
			Enabled:       enabled,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("update feed: %w", err)
	}

	var feed Feed
	feed.FromSQLCFeed(updated)
	return &feed, nil
}

// DeleteFeed removes a feed and all its entries.
func (s *Store) DeleteFeed(ctx context.Context, userID string, feedID string) error {
	if err := agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.DeleteFeed(ctx, sqlc.DeleteFeedParams{ID: feedID, UserID: userID})
	}); err != nil {
		return fmt.Errorf("delete feed: %w", err)
	}
	return nil
}

// CreateFeedEntry creates a new feed entry. Returns nil, nil when the entry
// already exists (ON CONFLICT DO NOTHING).
func (s *Store) CreateFeedEntry(ctx context.Context, feedID, guid, entryURL, title string) (*FeedEntry, error) {
	row, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyFeedEntry, error) {
		return q.CreateFeedEntry(ctx, sqlc.CreateFeedEntryParams{
			ID:           generateID(),
			FeedID:       feedID,
			Guid:         guid,
			Url:          entryURL,
			Title:        title,
			Status:       string(EntryStatusPending),
			ArticleID:    pgtype.Text{},
			Attempts:     0,
			ErrorMsg:     "",
			DiscoveredAt: time.Now().UTC(),
			ProcessedAt:  pgtype.Timestamptz{},
		})
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("create feed entry: %w", err)
	}
	var entry FeedEntry
	entry.FromSQLCFeedEntry(row)
	return &entry, nil
}

// ListPendingFeedEntries lists pending entries for processing.
func (s *Store) ListPendingFeedEntries(ctx context.Context, feedID string, limit, offset int) ([]FeedEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListPendingEntries(ctx, sqlc.ListPendingEntriesParams{FeedID: feedID, Limit: int32(limit), Offset: int32(offset)})
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

// GetFeedEntry retrieves a feed entry by ID.
func (s *Store) GetFeedEntry(ctx context.Context, feedID string, entryID string) (*FeedEntry, error) {
	row, err := s.q.GetFeedEntry(ctx, sqlc.GetFeedEntryParams{ID: entryID, FeedID: feedID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("feed entry not found: %s", entryID)
		}
		return nil, fmt.Errorf("get feed entry: %w", err)
	}
	var entry FeedEntry
	entry.FromSQLCFeedEntry(row)
	return &entry, nil
}

// MarkFeedEntry updates the status of a feed entry after processing.
func (s *Store) MarkFeedEntry(ctx context.Context, feedID string, entryID string, status RSSEntryStatus, articleID *string, errorMsg string) (*FeedEntry, error) {
	updated, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.RecallyFeedEntry, error) {
		return q.UpdateFeedEntry(ctx, sqlc.UpdateFeedEntryParams{
			ID:        entryID,
			FeedID:    feedID,
			Status:    string(status),
			ArticleID: toNullString(articleID),
			ErrorMsg:  errorMsg,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("update feed entry: %w", err)
	}
	var entry FeedEntry
	entry.FromSQLCFeedEntry(updated)
	return &entry, nil
}

// UpdateArticleFilePath updates only the file path of an article.
func (s *Store) UpdateArticleFilePath(ctx context.Context, userID, articleID, filePath string) error {
	_, err := s.UpdateArticle(ctx, userID, articleID, map[string]any{"file_path": filePath})
	if err != nil {
		return fmt.Errorf("update file path: %w", err)
	}
	return nil
}

func (s *Store) UpsertArticleContent(ctx context.Context, articleID, content string) error {
	if err := agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.UpsertArticleContent(ctx, sqlc.UpsertArticleContentParams{ArticleID: articleID, Content: content})
	}); err != nil {
		return fmt.Errorf("upsert article content: %w", err)
	}
	return nil
}

func (s *Store) InsertArticleContentIfAbsent(ctx context.Context, articleID, content string) error {
	if err := agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.InsertArticleContentIfAbsent(ctx, sqlc.InsertArticleContentIfAbsentParams{ArticleID: articleID, Content: content})
	}); err != nil {
		return fmt.Errorf("insert article content if absent: %w", err)
	}
	return nil
}

// ArticleMissingContent identifies a legacy article whose body still lives only
// in its disk mirror and has no recally_article_content row yet.
type ArticleMissingContent struct {
	ID       string
	FilePath string
}

// ListArticlesMissingContent returns legacy articles that carry a file_path but
// have no stored content row, so a startup job can copy their bodies into the DB.
func (s *Store) ListArticlesMissingContent(ctx context.Context) ([]ArticleMissingContent, error) {
	rows, err := s.q.ListArticlesMissingContent(ctx)
	if err != nil {
		return nil, fmt.Errorf("list articles missing content: %w", err)
	}
	out := make([]ArticleMissingContent, len(rows))
	for i, r := range rows {
		out[i] = ArticleMissingContent{ID: r.ID, FilePath: r.FilePath}
	}
	return out, nil
}

func (s *Store) GetArticleContent(ctx context.Context, articleID string) (string, bool, error) {
	content, err := s.q.GetArticleContent(ctx, articleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get article content: %w", err)
	}
	return content, true, nil
}

// GetDigest generates a daily reading digest for a user.
func (s *Store) GetDigest(ctx context.Context, userID string) (*Digest, error) {
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
		UserID: userID,
		Cutoff: time.Now().UTC().AddDate(0, 0, -3),
		Limit:  10,
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
	sort.Slice(digest.TopTags, func(i, j int) bool {
		return digest.TopTags[i].Count > digest.TopTags[j].Count
	})
	// Limit to top 10
	if len(digest.TopTags) > 10 {
		digest.TopTags = digest.TopTags[:10]
	}

	return digest, nil
}

// SaveDigest persists a daily digest snapshot. If one already exists for that
// date it is replaced. Counts are snapshotted from the live digest.
func (s *Store) SaveDigest(ctx context.Context, userID string, narrative, date string) (*StoredDigest, error) {
	live, err := s.GetDigest(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get live digest: %w", err)
	}

	topTagsJSON, err := json.Marshal(live.TopTags)
	if err != nil {
		return nil, fmt.Errorf("encode top_tags: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin digest transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := agentrun.ValidateTx(ctx, tx); err != nil {
		return nil, err
	}
	qtx := s.q.WithTx(tx)
	row, err := qtx.UpsertDigest(ctx, sqlc.UpsertDigestParams{
		ID:                   generateID(),
		UserID:               userID,
		Date:                 date,
		Narrative:            narrative,
		SavedYesterdayCount:  int64(live.SavedYesterdayCount),
		UnreadCount:          live.UnreadCount,
		ReadCount:            live.ReadCount,
		ArchivedCount:        live.ArchivedCount,
		StarredCount:         live.StarredCount,
		WorthRevisitingCount: int64(live.WorthRevisitingCount),
		TotalArticles:        live.TotalArticles,
		TopTags:              topTagsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert digest: %w", err)
	}

	// Replace article associations for both sections.
	for _, section := range []DigestSection{DigestSectionSavedYesterday, DigestSectionWorthRevisiting} {
		if err := qtx.DeleteDigestArticles(ctx, sqlc.DeleteDigestArticlesParams{
			DigestID: row.ID,
			Section:  section,
		}); err != nil {
			return nil, fmt.Errorf("clear digest articles: %w", err)
		}
	}

	articles := map[DigestSection][]Article{
		DigestSectionSavedYesterday:  live.SavedYesterday,
		DigestSectionWorthRevisiting: live.WorthRevisiting,
	}
	for section, list := range articles {
		for i, a := range list {
			if err := qtx.AddDigestArticle(ctx, sqlc.AddDigestArticleParams{
				DigestID:  row.ID,
				ArticleID: a.ID,
				Section:   section,
				Position:  int64(i),
			}); err != nil {
				return nil, fmt.Errorf("add digest article: %w", err)
			}
		}
	}

	stored, err := s.hydrateDigest(ctx, qtx, row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit digest transaction: %w", err)
	}
	return stored, nil
}

// GetStoredDigestByDate returns a persisted digest with full article objects.
func (s *Store) GetStoredDigestByDate(ctx context.Context, userID string, date string) (*StoredDigest, error) {
	row, err := s.q.GetDigestByDate(ctx, sqlc.GetDigestByDateParams{UserID: userID, Date: date})
	if err != nil {
		return nil, fmt.Errorf("get digest by date: %w", err)
	}
	return s.hydrateDigest(ctx, s.q, row)
}

// ListStoredDigests returns a paginated list of lightweight digest summaries.
func (s *Store) ListStoredDigests(ctx context.Context, userID string, limit, offset int64) ([]StoredDigestSummary, int64, error) {
	total, err := s.q.CountDigests(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("count digests: %w", err)
	}
	rows, err := s.q.ListDigests(ctx, sqlc.ListDigestsParams{
		UserID: userID,
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list digests: %w", err)
	}
	summaries := make([]StoredDigestSummary, 0, len(rows))
	for _, r := range rows {
		summaries = append(summaries, StoredDigestSummary{
			ID:                   r.ID,
			Date:                 r.Date,
			Narrative:            r.Narrative,
			SavedYesterdayCount:  r.SavedYesterdayCount,
			WorthRevisitingCount: r.WorthRevisitingCount,
			TotalArticles:        r.TotalArticles,
			CreatedAt:            r.CreatedAt.UTC(),
		})
	}
	return summaries, total, nil
}

func (s *Store) hydrateDigest(ctx context.Context, q *sqlc.Queries, row sqlc.RecallyDigest) (*StoredDigest, error) {
	savedYesterday, err := s.loadDigestArticles(ctx, q, row.ID, DigestSectionSavedYesterday)
	if err != nil {
		return nil, err
	}
	worthRevisiting, err := s.loadDigestArticles(ctx, q, row.ID, DigestSectionWorthRevisiting)
	if err != nil {
		return nil, err
	}
	topTags := decodeTopTags(row.TopTags)

	return &StoredDigest{
		ID:                   row.ID,
		UserID:               row.UserID,
		Date:                 row.Date,
		Narrative:            row.Narrative,
		SavedYesterday:       savedYesterday,
		SavedYesterdayCount:  row.SavedYesterdayCount,
		UnreadCount:          row.UnreadCount,
		ReadCount:            row.ReadCount,
		ArchivedCount:        row.ArchivedCount,
		StarredCount:         row.StarredCount,
		WorthRevisiting:      worthRevisiting,
		WorthRevisitingCount: row.WorthRevisitingCount,
		TopTags:              topTags,
		TotalArticles:        row.TotalArticles,
		CreatedAt:            row.CreatedAt.UTC(),
		UpdatedAt:            row.UpdatedAt.UTC(),
	}, nil
}

func (s *Store) loadDigestArticles(ctx context.Context, q *sqlc.Queries, digestID, section string) ([]Article, error) {
	rows, err := q.ListDigestArticles(ctx, sqlc.ListDigestArticlesParams{
		DigestID: digestID,
		Section:  section,
	})
	if err != nil {
		return nil, fmt.Errorf("list digest articles (%s): %w", section, err)
	}
	articles := make([]Article, 0, len(rows))
	for _, r := range rows {
		var a Article
		a.FromSQLCArticle(r)
		articles = append(articles, a)
	}
	return articles, nil
}

func decodeTopTags(value json.RawMessage) []TagCount {
	var tags []TagCount
	if err := json.Unmarshal(value, &tags); err != nil {
		return []TagCount{}
	}
	if tags == nil {
		return []TagCount{}
	}
	return tags
}

func emptyOrString(value string) any {
	if value == "" {
		return ""
	}
	return value
}

func toNullString(value *string) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func toNullTime(value *time.Time) pgtype.Timestamptz {
	if value == nil || value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
