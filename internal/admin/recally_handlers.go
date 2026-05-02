package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/recally"
)

// recallyHandlers implements the generated recally ServerInterface and is the
// single entry point through which CLI/web/SDK clients reach the recally store
// and on-disk markdown library. All bodies live on the server; clients are
// purely HTTP.
type recallyHandlers struct {
	store *recally.Store
	files *recally.FileManager
	feeds *gofeed.Parser
}

func newRecallyHandlers(store *recally.Store, files *recally.FileManager) *recallyHandlers {
	return &recallyHandlers{store: store, files: files, feeds: gofeed.NewParser()}
}

// requireUser enforces bearer/session auth and returns the user ID. It writes
// the 401 response itself so callers can return early.
func (h *recallyHandlers) requireUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return 0, false
	}
	return info.UserID, true
}

// articleOwned loads an article and returns it only if the caller owns it.
// Returns false (with the appropriate error response written) otherwise.
func (h *recallyHandlers) articleOwned(w http.ResponseWriter, ctx context.Context, articleID string, userID int64) (*recally.Article, bool) {
	article, err := h.store.GetArticle(ctx, articleID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	if article.UserID != userID {
		writeError(w, http.StatusNotFound, "article not found")
		return nil, false
	}
	return article, true
}

func (h *recallyHandlers) feedOwned(w http.ResponseWriter, ctx context.Context, feedID string, userID int64) (*recally.Feed, bool) {
	feed, err := h.store.GetFeed(ctx, feedID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	if feed.UserID != userID {
		writeError(w, http.StatusNotFound, "feed not found")
		return nil, false
	}
	return feed, true
}

// ----------------------------- articles -------------------------------------

func (h *recallyHandlers) ListArticles(w http.ResponseWriter, r *http.Request, params ListArticlesParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	limit := 50
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}

	if params.CanonicalUrl != nil && *params.CanonicalUrl != "" {
		article, err := h.store.GetArticleByCanonicalURL(ctx, userID, *params.CanonicalUrl)
		if err != nil {
			writeJSON(w, http.StatusOK, ArticleList{Items: []Article{}})
			return
		}
		writeJSON(w, http.StatusOK, ArticleList{Items: []Article{toAPIArticle(article, "")}})
		return
	}

	if params.Q != nil && *params.Q != "" {
		articles, err := h.store.SearchArticles(ctx, userID, *params.Q, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ArticleList{Items: toAPIArticles(articles)})
		return
	}

	filter := recally.ArticleFilter{Limit: limit}
	if params.Status != nil {
		filter.Status = recally.ArticleStatus(*params.Status)
	}
	if params.SourceType != nil {
		filter.SourceType = recally.SourceType(*params.SourceType)
	}
	if params.Starred != nil {
		filter.Starred = params.Starred
	}
	articles, err := h.store.ListArticles(ctx, userID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ArticleList{Items: toAPIArticles(articles)})
}

func (h *recallyHandlers) SaveArticle(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body SaveArticleRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	canonical := strDeref(body.CanonicalUrl)
	if canonical == "" {
		canonical = recally.NormalizeURL(body.Url)
	}

	// Look up existing first so we can decide between create and metadata-only update,
	// and resolve content from the existing file when none was sent.
	existing, lookupErr := h.store.GetArticleByCanonicalURL(r.Context(), userID, canonical)
	content := strDeref(body.Content)
	if content == "" && lookupErr != nil {
		writeError(w, http.StatusBadRequest, "content is required for new articles")
		return
	}

	sourceType := recally.SourceType("web")
	if body.SourceType != nil {
		sourceType = recally.SourceType(*body.SourceType)
	}
	req := recally.SaveRequest{
		URL:          body.Url,
		CanonicalURL: canonical,
		SourceType:   sourceType,
		Title:        strDeref(body.Title),
		Author:       strDeref(body.Author),
		Summary:      strDeref(body.Summary),
		Tags:         strSliceDeref(body.Tags),
		Content:      content,
		Metadata:     mapDeref(body.Metadata),
		PublishedAt:  body.PublishedAt,
		AgentID:      body.AgentId,
	}

	article, isNew, err := h.store.SaveArticle(r.Context(), userID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If new, write the markdown file and persist the relative path. If the
	// caller is updating an existing article without supplying new content, we
	// rewrite the existing file with the refreshed frontmatter so it stays in
	// sync with DB metadata.
	annaHome := config.AnnaHome()
	var filePath string
	switch {
	case isNew:
		filePath = h.files.ArticlePath(userID, article.ID, article.Title, article.SavedAt)
	case existing != nil && existing.FilePath != "":
		filePath = existing.FilePath
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(annaHome, filePath)
		}
		if content == "" {
			body, readErr := h.files.ReadArticle(filePath)
			if readErr == nil {
				content = body
			}
		}
	default:
		filePath = h.files.ArticlePath(userID, article.ID, article.Title, article.SavedAt)
	}
	if err := h.files.WriteArticle(filePath, article, content); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write article file: %v", err))
		return
	}
	relPath := h.files.RelativePath(filePath)
	if err := h.store.UpdateArticleFilePath(r.Context(), article.ID, relPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	article.FilePath = relPath

	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/recally/articles/"+article.ID)
	}
	writeJSON(w, status, toAPIArticle(article, ""))
}

func (h *recallyHandlers) GetArticle(w http.ResponseWriter, r *http.Request, id string, params GetArticleParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.articleOwned(w, r.Context(), id, userID)
	if !ok {
		return
	}
	include := ""
	if params.Include != nil {
		include = *params.Include
	}
	if includesContent(include) {
		body, _ := h.readArticleBody(article)
		writeJSON(w, http.StatusOK, toAPIArticle(article, body))
		return
	}
	writeJSON(w, http.StatusOK, toAPIArticle(article, ""))
}

func (h *recallyHandlers) UpdateArticle(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.articleOwned(w, r.Context(), id, userID)
	if !ok {
		return
	}
	var body UpdateArticleRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	updates := map[string]any{}
	if body.Title != nil {
		updates["title"] = *body.Title
	}
	if body.Author != nil {
		updates["author"] = *body.Author
	}
	if body.Summary != nil {
		updates["summary"] = *body.Summary
	}
	if body.Tags != nil {
		updates["tags"] = *body.Tags
	}
	if body.Status != nil {
		updates["status"] = string(*body.Status)
	}
	if body.Starred != nil {
		updates["starred"] = *body.Starred
	}
	if body.FilePath != nil {
		updates["file_path"] = *body.FilePath
	}
	if body.Metadata != nil {
		updates["metadata"] = *body.Metadata
	}
	if body.PublishedAt != nil {
		updates["published_at"] = body.PublishedAt
	}

	updated := article
	if len(updates) > 0 {
		next, err := h.store.UpdateArticle(r.Context(), id, updates)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		updated = next
	}

	if body.Content != nil || len(updates) > 0 {
		if err := h.rewriteArticleFile(updated, strDeref(body.Content)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, toAPIArticle(updated, ""))
}

func (h *recallyHandlers) DeleteArticle(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.articleOwned(w, r.Context(), id, userID)
	if !ok {
		return
	}
	if err := h.store.DeleteArticle(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if article.FilePath != "" {
		path := article.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(config.AnnaHome(), path)
		}
		_ = h.files.DeleteArticle(path)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------- feeds --------------------------------------

func (h *recallyHandlers) ListFeeds(w http.ResponseWriter, r *http.Request, params ListFeedsParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if params.Url != nil && *params.Url != "" {
		feed, err := h.store.GetFeedByURL(r.Context(), userID, *params.Url)
		if err != nil {
			writeJSON(w, http.StatusOK, FeedList{Items: []Feed{}})
			return
		}
		writeJSON(w, http.StatusOK, FeedList{Items: []Feed{toAPIFeed(feed)}})
		return
	}
	feeds, err := h.store.ListFeeds(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]Feed, 0, len(feeds))
	for i := range feeds {
		items = append(items, toAPIFeed(&feeds[i]))
	}
	writeJSON(w, http.StatusOK, FeedList{Items: items})
}

func (h *recallyHandlers) CreateFeed(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body CreateFeedRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if existing, _ := h.store.GetFeedByURL(r.Context(), userID, body.Url); existing != nil {
		writeError(w, http.StatusConflict, "feed already subscribed")
		return
	}
	title := strDeref(body.Title)
	description := ""
	parseCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	parsed, err := h.feeds.ParseURLWithContext(body.Url, parseCtx)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch feed: %v", err))
		return
	}
	if title == "" {
		title = parsed.Title
	}
	description = parsed.Description

	feed, err := h.store.CreateFeed(r.Context(), userID, body.Url, title, description, body.AgentId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Location", "/api/recally/feeds/"+feed.ID)
	writeJSON(w, http.StatusCreated, toAPIFeed(feed))
}

func (h *recallyHandlers) GetFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	feed, ok := h.feedOwned(w, r.Context(), id, userID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAPIFeed(feed))
}

func (h *recallyHandlers) UpdateFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.feedOwned(w, r.Context(), id, userID); !ok {
		return
	}
	var body UpdateFeedRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updates := map[string]any{}
	if body.Title != nil {
		updates["title"] = *body.Title
	}
	if body.Description != nil {
		updates["description"] = *body.Description
	}
	if body.CheckInterval != nil {
		updates["check_interval"] = *body.CheckInterval
	}
	if body.Enabled != nil {
		updates["enabled"] = *body.Enabled
	}
	updated, err := h.store.UpdateFeed(r.Context(), id, updates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIFeed(updated))
}

func (h *recallyHandlers) DeleteFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.feedOwned(w, r.Context(), id, userID); !ok {
		return
	}
	if err := h.store.DeleteFeed(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *recallyHandlers) PollFeed(w http.ResponseWriter, r *http.Request, id string, params PollFeedParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	feed, ok := h.feedOwned(w, r.Context(), id, userID)
	if !ok {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	result := FeedPollResult{Feed: toAPIFeed(feed), NewEntries: []FeedEntry{}}
	if !feed.Enabled {
		writeJSON(w, http.StatusOK, result)
		return
	}

	parseCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	parsed, err := h.feeds.ParseURLWithContext(feed.URL, parseCtx)
	cancel()
	if err != nil {
		msg := err.Error()
		result.Error = &msg
		writeJSON(w, http.StatusOK, result)
		return
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
		entry, cerr := h.store.CreateFeedEntry(r.Context(), feed.ID, guid, entryURL, item.Title)
		if cerr != nil || entry == nil {
			continue
		}
		result.NewEntries = append(result.NewEntries, toAPIFeedEntry(entry))
		if len(result.NewEntries) >= limit {
			break
		}
	}

	now := time.Now().UTC()
	if updated, err := h.store.UpdateFeed(r.Context(), feed.ID, map[string]any{"last_checked_at": &now}); err == nil {
		result.Feed = toAPIFeed(updated)
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------- feed entries ----------------------------------

func (h *recallyHandlers) ListFeedEntries(w http.ResponseWriter, r *http.Request, feedId string, params ListFeedEntriesParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.feedOwned(w, r.Context(), feedId, userID); !ok {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	status := recally.EntryStatusPending
	if params.Status != nil {
		status = recally.RSSEntryStatus(*params.Status)
	}
	if status != recally.EntryStatusPending {
		// store currently exposes only pending listing; reject other filters explicitly
		writeError(w, http.StatusBadRequest, "only status=pending is supported")
		return
	}
	entries, err := h.store.ListPendingFeedEntries(r.Context(), feedId, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]FeedEntry, 0, len(entries))
	for i := range entries {
		items = append(items, toAPIFeedEntry(&entries[i]))
	}
	writeJSON(w, http.StatusOK, FeedEntryList{Items: items})
}

func (h *recallyHandlers) UpdateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	entry, err := h.store.GetFeedEntry(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if entry.FeedID != feedId {
		writeError(w, http.StatusNotFound, "entry not found in this feed")
		return
	}
	if _, ok := h.feedOwned(w, r.Context(), feedId, userID); !ok {
		return
	}
	var body UpdateFeedEntryRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status := recally.RSSEntryStatus(body.Status)
	switch status {
	case recally.EntryStatusSaved, recally.EntryStatusSkipped, recally.EntryStatusError, recally.EntryStatusPending:
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if status == recally.EntryStatusSaved && (body.ArticleId == nil || *body.ArticleId == "") {
		writeError(w, http.StatusBadRequest, "article_id required when status=saved")
		return
	}
	updated, err := h.store.MarkFeedEntry(r.Context(), id, status, body.ArticleId, strDeref(body.ErrorMsg))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIFeedEntry(updated))
}

// ------------------------------- digest -------------------------------------

func (h *recallyHandlers) GetDigest(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	digest, err := h.store.GetDigest(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIDigest(digest))
}

// ------------------------------ helpers -------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func includesContent(include string) bool {
	for _, part := range strings.Split(include, ",") {
		if strings.TrimSpace(part) == "content" {
			return true
		}
	}
	return false
}

func (h *recallyHandlers) readArticleBody(article *recally.Article) (string, error) {
	if article.FilePath == "" {
		return "", errors.New("article has no file")
	}
	path := article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.AnnaHome(), path)
	}
	return h.files.ReadArticle(path)
}

func (h *recallyHandlers) rewriteArticleFile(article *recally.Article, newContent string) error {
	if article.FilePath == "" {
		return nil
	}
	path := article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.AnnaHome(), path)
	}
	body := newContent
	if body == "" {
		existing, err := h.files.ReadArticle(path)
		if err != nil {
			return nil // file may not exist locally; skip silently
		}
		body = existing
	}
	return h.files.WriteArticle(path, article, body)
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strSliceDeref(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func mapDeref(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return *p
}

func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ------------------------- API model conversions ----------------------------

func toAPIArticle(a *recally.Article, content string) Article {
	out := Article{
		Id:           a.ID,
		AgentId:      a.AgentID,
		Url:          a.URL,
		CanonicalUrl: a.CanonicalURL,
		SourceType:   SourceType(a.SourceType),
		Title:        a.Title,
		Author:       ptrStr(a.Author),
		Summary:      ptrStr(a.Summary),
		Status:       ArticleStatus(a.Status),
		Starred:      a.Starred,
		FilePath:     a.FilePath,
		PublishedAt:  a.PublishedAt,
		SavedAt:      a.SavedAt,
		ReadAt:       a.ReadAt,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
	if len(a.Tags) > 0 {
		tags := append([]string(nil), a.Tags...)
		out.Tags = &tags
	}
	if len(a.Metadata) > 0 {
		md := make(map[string]string, len(a.Metadata))
		for k, v := range a.Metadata {
			md[k] = v
		}
		out.Metadata = &md
	}
	if content != "" {
		out.Content = &content
	}
	return out
}

func toAPIArticles(articles []recally.Article) []Article {
	items := make([]Article, 0, len(articles))
	for i := range articles {
		items = append(items, toAPIArticle(&articles[i], ""))
	}
	return items
}

func toAPIFeed(f *recally.Feed) Feed {
	return Feed{
		Id:            f.ID,
		AgentId:       f.AgentID,
		Url:           f.URL,
		Title:         f.Title,
		Description:   ptrStr(f.Description),
		CheckInterval: f.CheckInterval,
		LastCheckedAt: f.LastCheckedAt,
		LastEtag:      ptrStr(f.LastETag),
		LastModified:  ptrStr(f.LastModified),
		Enabled:       f.Enabled,
		CreatedAt:     f.CreatedAt,
		UpdatedAt:     f.UpdatedAt,
	}
}

func toAPIFeedEntry(e *recally.FeedEntry) FeedEntry {
	return FeedEntry{
		Id:           e.ID,
		FeedId:       e.FeedID,
		Guid:         e.GUID,
		Url:          e.URL,
		Title:        e.Title,
		Status:       FeedEntryStatus(e.Status),
		ArticleId:    e.ArticleID,
		Attempts:     e.Attempts,
		ErrorMsg:     ptrStr(e.ErrorMsg),
		DiscoveredAt: e.DiscoveredAt,
		ProcessedAt:  e.ProcessedAt,
	}
}

func toAPIDigest(d *recally.Digest) Digest {
	out := Digest{
		Date:                 d.Date,
		SavedYesterday:       toAPIArticles(d.SavedYesterday),
		SavedYesterdayCount:  d.SavedYesterdayCount,
		UnreadCount:          d.UnreadCount,
		ReadCount:            d.ReadCount,
		ArchivedCount:        d.ArchivedCount,
		StarredCount:         d.StarredCount,
		WorthRevisiting:      toAPIArticles(d.WorthRevisiting),
		WorthRevisitingCount: d.WorthRevisitingCount,
		TotalArticles:        d.TotalArticles,
		TopTags:              make([]TagCount, 0, len(d.TopTags)),
	}
	for _, t := range d.TopTags {
		out.TopTags = append(out.TopTags, TagCount{Tag: t.Tag, Count: t.Count})
	}
	return out
}
