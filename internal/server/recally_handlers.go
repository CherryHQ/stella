package server

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/recally"
)

// recallyHandlers implements the recally portion of apiserver.ServerInterface and is the
// single entry point through which CLI/web/SDK clients reach the recally store
// and on-disk markdown library. All bodies live on the server; clients are
// purely HTTP.
type recallyHandlers struct {
	store *recally.Store
	files *recally.FileManager
	svc   *recally.Service
	feeds *gofeed.Parser
	log   *slog.Logger
}

func newRecallyHandlersWithService(store *recally.Store, files *recally.FileManager, svc *recally.Service, log *slog.Logger) *recallyHandlers {
	return &recallyHandlers{store: store, files: files, svc: svc, feeds: gofeed.NewParser(), log: log.With("component", "recally-api")}
}

func (h *recallyHandlers) writeInternalError(w http.ResponseWriter, err error) {
	writeLoggedError(w, h.log, http.StatusInternalServerError, "internal error", err)
}

func (h *recallyHandlers) writeBadGatewayError(w http.ResponseWriter, err error) {
	writeLoggedError(w, h.log, http.StatusBadGateway, "upstream service error", err)
}

// requireUser enforces bearer/session auth and returns the user ID. It writes
// the 401 response itself so callers can return early.
func (h *recallyHandlers) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	return info.UserID, true
}

// loadArticle loads an article and returns it only if the caller owns it.
// Returns false (with the appropriate error response written) otherwise.
func (h *recallyHandlers) loadArticle(w http.ResponseWriter, ctx context.Context, articleID string, userID string) (*recally.Article, bool) {
	article, err := h.store.GetArticle(ctx, userID, articleID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			h.writeInternalError(w, err)
		}
		return nil, false
	}
	if article.UserID != userID {
		writeError(w, http.StatusNotFound, "article not found")
		return nil, false
	}
	return article, true
}

func (h *recallyHandlers) loadFeed(w http.ResponseWriter, ctx context.Context, feedID string, userID string) (*recally.Feed, bool) {
	feed, err := h.store.GetFeed(ctx, userID, feedID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			h.writeInternalError(w, err)
		}
		return nil, false
	}
	if feed.UserID != userID {
		writeError(w, http.StatusNotFound, "feed not found")
		return nil, false
	}
	return feed, true
}

// ----------------------------- articles -------------------------------------

func (h *recallyHandlers) ListArticles(w http.ResponseWriter, r *http.Request, params apiserver.ListArticlesParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	if params.CanonicalUrl != nil && *params.CanonicalUrl != "" {
		article, err := h.store.GetArticleByCanonicalURL(ctx, userID, *params.CanonicalUrl)
		if err != nil {
			writeData(w, http.StatusOK, apiserver.ArticleList{Articles: []apiserver.Article{}})
			return
		}
		writeData(w, http.StatusOK, apiserver.ArticleList{Articles: []apiserver.Article{toAPIArticle(article, "")}})
		return
	}

	if params.Q != nil && *params.Q != "" {
		articles, err := h.store.SearchArticles(ctx, userID, *params.Q, limit)
		if err != nil {
			h.writeInternalError(w, err)
			return
		}
		writeData(w, http.StatusOK, apiserver.ArticleList{Articles: toAPIArticles(articles)})
		return
	}

	filter := recally.ArticleFilter{Limit: limit + 1, Offset: offset}
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
		h.writeInternalError(w, err)
		return
	}
	articles, nextToken := nextPageTokenForRows(articles, limit, offset)
	list := apiserver.ArticleList{Articles: toAPIArticles(articles)}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (h *recallyHandlers) SaveArticle(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body apiserver.SaveArticleRequest
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
		Content:      strDeref(body.Content),
		Metadata:     mapDeref(body.Metadata),
		PublishedAt:  body.PublishedAt,
		AgentID:      body.AgentId,
	}

	result, err := h.svc.As(authz.Identity{UserID: userID}).Save(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "content is required") {
			writeError(w, http.StatusBadRequest, "content is required for new articles")
			return
		}
		h.writeInternalError(w, err)
		return
	}
	article, isNew := result.Article, result.Created

	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/recally/articles/"+article.ID)
	}
	writeData(w, status, toAPIArticle(article, ""))
}

func (h *recallyHandlers) GetArticle(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetArticleParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.loadArticle(w, r.Context(), id, userID)
	if !ok {
		return
	}
	include := ""
	if params.Include != nil {
		include = *params.Include
	}
	if includesContent(include) {
		body, _ := h.readArticleBody(article)
		writeData(w, http.StatusOK, toAPIArticle(article, body))
		return
	}
	writeData(w, http.StatusOK, toAPIArticle(article, ""))
}

func (h *recallyHandlers) UpdateArticle(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.loadArticle(w, r.Context(), id, userID)
	if !ok {
		return
	}
	var body apiserver.UpdateArticleRequest
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
		next, err := h.store.UpdateArticle(r.Context(), userID, id, updates)
		if err != nil {
			h.writeInternalError(w, err)
			return
		}
		updated = next
	}

	if body.Content != nil || len(updates) > 0 {
		if err := h.rewriteArticleFile(updated, strDeref(body.Content)); err != nil {
			h.writeInternalError(w, err)
			return
		}
	}

	writeData(w, http.StatusOK, toAPIArticle(updated, ""))
}

func (h *recallyHandlers) DeleteArticle(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	article, ok := h.loadArticle(w, r.Context(), id, userID)
	if !ok {
		return
	}
	if err := h.store.DeleteArticle(r.Context(), userID, id); err != nil {
		h.writeInternalError(w, err)
		return
	}
	if article.FilePath != "" {
		path := article.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(config.StellaHome(), path)
		}
		_ = h.files.DeleteArticle(path)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------- feeds --------------------------------------

func (h *recallyHandlers) ListFeeds(w http.ResponseWriter, r *http.Request, params apiserver.ListFeedsParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if params.Url != nil && *params.Url != "" {
		feed, err := h.store.GetFeedByURL(r.Context(), userID, *params.Url)
		if err != nil {
			writeData(w, http.StatusOK, apiserver.FeedList{Feeds: []apiserver.Feed{}})
			return
		}
		writeData(w, http.StatusOK, apiserver.FeedList{Feeds: []apiserver.Feed{toAPIFeed(feed)}})
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	feeds, err := h.store.ListFeeds(r.Context(), userID, limit+1, offset)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	feeds, nextToken := nextPageTokenForRows(feeds, limit, offset)
	items := make([]apiserver.Feed, 0, len(feeds))
	for i := range feeds {
		items = append(items, toAPIFeed(&feeds[i]))
	}
	list := apiserver.FeedList{Feeds: items}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (h *recallyHandlers) CreateFeed(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body apiserver.CreateFeedRequest
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

	kind := recally.SniffFeedKind(body.Url)
	if body.Kind != nil {
		kind = recally.FeedKind(*body.Kind)
	}
	if !kind.Valid() {
		writeError(w, http.StatusBadRequest, "invalid feed kind")
		return
	}
	if err := recally.ValidateFeedSubscription(body.Url, kind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	title := strDeref(body.Title)
	description := ""
	// Only rss kinds are parsed server-side; skill-driven kinds backfill metadata later.
	if kind == recally.FeedKindRSS {
		parseCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		parsed, err := h.feeds.ParseURLWithContext(body.Url, parseCtx)
		cancel()
		if err != nil {
			h.writeBadGatewayError(w, err)
			return
		}
		if title == "" {
			title = parsed.Title
		}
		description = parsed.Description
	}

	feed, err := h.store.CreateFeed(r.Context(), userID, body.Url, kind, nil, title, description, body.AgentId)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	w.Header().Set("Location", "/api/recally/feeds/"+feed.ID)
	writeData(w, http.StatusCreated, toAPIFeed(feed))
}

func (h *recallyHandlers) GetFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	feed, ok := h.loadFeed(w, r.Context(), id, userID)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, toAPIFeed(feed))
}

func (h *recallyHandlers) UpdateFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadFeed(w, r.Context(), id, userID); !ok {
		return
	}
	var body apiserver.UpdateFeedRequest
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
	updated, err := h.store.UpdateFeed(r.Context(), userID, id, updates)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIFeed(updated))
}

func (h *recallyHandlers) DeleteFeed(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadFeed(w, r.Context(), id, userID); !ok {
		return
	}
	if err := h.store.DeleteFeed(r.Context(), userID, id); err != nil {
		h.writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *recallyHandlers) PollFeed(w http.ResponseWriter, r *http.Request, id string, params apiserver.PollFeedParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	feed, ok := h.loadFeed(w, r.Context(), id, userID)
	if !ok {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	result := apiserver.FeedPollResult{Feed: toAPIFeed(feed), NewEntries: []apiserver.FeedEntry{}}
	if !feed.Enabled {
		writeData(w, http.StatusOK, result)
		return
	}
	// Only rss feeds are polled server-side; skill-driven kinds discover entries
	// via the recally extraction workflow, so this is a no-op for them.
	if feed.Kind != recally.FeedKindRSS {
		writeData(w, http.StatusOK, result)
		return
	}

	parseCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	parsed, err := h.feeds.ParseURLWithContext(feed.URL, parseCtx)
	cancel()
	if err != nil {
		h.log.Warn("feed poll upstream error", "feed_id", feed.ID, "url", feed.URL, "error", err)
		msg := "failed to fetch feed"
		result.Error = &msg
		writeData(w, http.StatusOK, result)
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
	if updated, err := h.store.UpdateFeed(r.Context(), userID, feed.ID, map[string]any{"last_checked_at": &now}); err == nil {
		result.Feed = toAPIFeed(updated)
	} else {
		slog.Warn("failed to update feed last_checked_at", "feed_id", feed.ID, "error", err)
	}
	writeData(w, http.StatusOK, result)
}

// ---------------------------- feed entries ----------------------------------

func (h *recallyHandlers) ListFeedEntries(w http.ResponseWriter, r *http.Request, feedId string, params apiserver.ListFeedEntriesParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadFeed(w, r.Context(), feedId, userID); !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
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
	entries, err := h.store.ListPendingFeedEntries(r.Context(), feedId, limit+1, offset)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	entries, nextToken := nextPageTokenForRows(entries, limit, offset)
	items := make([]apiserver.FeedEntry, 0, len(entries))
	for i := range entries {
		items = append(items, toAPIFeedEntry(&entries[i]))
	}
	list := apiserver.FeedEntryList{Entries: items}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (h *recallyHandlers) CreateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadFeed(w, r.Context(), feedId, userID); !ok {
		return
	}
	var body apitypes.CreateFeedEntryRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Guid == "" {
		writeError(w, http.StatusBadRequest, "guid is required")
		return
	}
	entry, err := h.store.CreateFeedEntry(r.Context(), feedId, body.Guid, strDeref(body.Url), strDeref(body.Title))
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	result := apitypes.CreateFeedEntryResult{Created: entry != nil}
	if entry != nil {
		apiEntry := toAPIFeedEntry(entry)
		result.Entry = &apiEntry
	}
	writeData(w, http.StatusOK, result)
}

func (h *recallyHandlers) UpdateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string, id string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadFeed(w, r.Context(), feedId, userID); !ok {
		return
	}
	entry, err := h.store.GetFeedEntry(r.Context(), feedId, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if entry.FeedID != feedId {
		writeError(w, http.StatusNotFound, "entry not found in this feed")
		return
	}
	var body apiserver.UpdateFeedEntryRequest
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
	updated, err := h.store.MarkFeedEntry(r.Context(), feedId, id, status, body.ArticleId, strDeref(body.ErrorMsg))
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIFeedEntry(updated))
}

// ------------------------------- digest -------------------------------------

func (h *recallyHandlers) GetDigest(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	digest, err := h.store.GetDigest(r.Context(), userID)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIDigest(digest))
}

func (h *recallyHandlers) ListStoredDigests(w http.ResponseWriter, r *http.Request, params apiserver.ListStoredDigestsParams) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	limitInt, offsetInt, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	limit, offset := int64(limitInt), int64(offsetInt)
	summaries, total, err := h.store.ListStoredDigests(r.Context(), userID, limit, offset)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	items := make([]apitypes.StoredDigestSummary, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, toAPIStoredDigestSummary(s))
	}
	list := apitypes.StoredDigestSummaryList{Digests: items, TotalSize: total}
	if offset+int64(len(summaries)) < total {
		tok := encodeOffsetToken(int(offset) + len(summaries))
		list.NextPageToken = &tok
	}
	writeData(w, http.StatusOK, list)
}

func (h *recallyHandlers) SaveDigest(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body apitypes.SaveDigestRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Narrative == "" {
		writeError(w, http.StatusBadRequest, "narrative is required")
		return
	}
	date := time.Now().UTC().Format("2006-01-02")
	if body.Date != nil && *body.Date != "" {
		date = *body.Date
	}
	stored, err := h.store.SaveDigest(r.Context(), userID, body.Narrative, date)
	if err != nil {
		h.writeInternalError(w, err)
		return
	}
	w.Header().Set("Location", "/api/recally/digests/"+stored.Date)
	writeData(w, http.StatusCreated, toAPIStoredDigest(stored))
}

func (h *recallyHandlers) GetStoredDigest(w http.ResponseWriter, r *http.Request, date string) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	stored, err := h.store.GetStoredDigestByDate(r.Context(), userID, date)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "digest not found")
		} else {
			h.writeInternalError(w, err)
		}
		return
	}
	writeData(w, http.StatusOK, toAPIStoredDigest(stored))
}

// ------------------------------ helpers -------------------------------------

func includesContent(include string) bool {
	for part := range strings.SplitSeq(include, ",") {
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
		path = filepath.Join(config.StellaHome(), path)
	}
	return h.files.ReadArticle(path)
}

func (h *recallyHandlers) rewriteArticleFile(article *recally.Article, newContent string) error {
	if article.FilePath == "" {
		return nil
	}
	path := article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.StellaHome(), path)
	}
	body := newContent
	if body == "" {
		existing, err := h.files.ReadArticle(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
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

func toAPIArticle(a *recally.Article, content string) apiserver.Article {
	out := apiserver.Article{
		Id:           a.ID,
		AgentId:      a.AgentID,
		Url:          a.URL,
		CanonicalUrl: a.CanonicalURL,
		SourceType:   apiserver.SourceType(a.SourceType),
		Title:        a.Title,
		Author:       ptrStr(a.Author),
		Summary:      ptrStr(a.Summary),
		Status:       apiserver.ArticleStatus(a.Status),
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
		maps.Copy(md, a.Metadata)
		out.Metadata = &md
	}
	if content != "" {
		out.Content = &content
	}
	return out
}

func toAPIArticles(articles []recally.Article) []apiserver.Article {
	items := make([]apiserver.Article, 0, len(articles))
	for i := range articles {
		items = append(items, toAPIArticle(&articles[i], ""))
	}
	return items
}

func toAPIFeed(f *recally.Feed) apiserver.Feed {
	var metadata *map[string]string
	if len(f.Metadata) > 0 {
		m := f.Metadata
		metadata = &m
	}
	return apiserver.Feed{
		Id:            f.ID,
		AgentId:       f.AgentID,
		Url:           f.URL,
		Kind:          apitypes.FeedKind(f.Kind),
		Metadata:      metadata,
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

func toAPIFeedEntry(e *recally.FeedEntry) apiserver.FeedEntry {
	return apiserver.FeedEntry{
		Id:           e.ID,
		FeedId:       e.FeedID,
		Guid:         e.GUID,
		Url:          e.URL,
		Title:        e.Title,
		Status:       apiserver.FeedEntryStatus(e.Status),
		ArticleId:    e.ArticleID,
		Attempts:     e.Attempts,
		ErrorMsg:     ptrStr(e.ErrorMsg),
		DiscoveredAt: e.DiscoveredAt,
		ProcessedAt:  e.ProcessedAt,
	}
}

func toAPIStoredDigest(d *recally.StoredDigest) apitypes.StoredDigest {
	out := apitypes.StoredDigest{
		Id:                   d.ID,
		Date:                 d.Date,
		Narrative:            d.Narrative,
		SavedYesterday:       toAPIArticles(d.SavedYesterday),
		SavedYesterdayCount:  d.SavedYesterdayCount,
		UnreadCount:          d.UnreadCount,
		ReadCount:            d.ReadCount,
		ArchivedCount:        d.ArchivedCount,
		StarredCount:         d.StarredCount,
		WorthRevisiting:      toAPIArticles(d.WorthRevisiting),
		WorthRevisitingCount: d.WorthRevisitingCount,
		TotalArticles:        d.TotalArticles,
		CreatedAt:            d.CreatedAt,
		UpdatedAt:            d.UpdatedAt,
		TopTags:              make([]apiserver.TagCount, 0, len(d.TopTags)),
	}
	for _, t := range d.TopTags {
		out.TopTags = append(out.TopTags, apiserver.TagCount{Tag: t.Tag, Count: t.Count})
	}
	return out
}

func toAPIStoredDigestSummary(s recally.StoredDigestSummary) apitypes.StoredDigestSummary {
	return apitypes.StoredDigestSummary{
		Id:                   s.ID,
		Date:                 s.Date,
		Narrative:            s.Narrative,
		SavedYesterdayCount:  s.SavedYesterdayCount,
		WorthRevisitingCount: s.WorthRevisitingCount,
		TotalArticles:        s.TotalArticles,
		CreatedAt:            s.CreatedAt,
	}
}

func toAPIDigest(d *recally.Digest) apiserver.Digest {
	out := apiserver.Digest{
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
		TopTags:              make([]apiserver.TagCount, 0, len(d.TopTags)),
	}
	for _, t := range d.TopTags {
		out.TopTags = append(out.TopTags, apiserver.TagCount{Tag: t.Tag, Count: t.Count})
	}
	return out
}
