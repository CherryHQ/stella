package server

import (
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/recally"
)

// recallyHandlers implements the recally portion of apiserver.ServerInterface and is the
// single entry point through which CLI/web/SDK clients reach the recally library.
// The recally Service is the sole policy-enforcement point: every handler derives
// the caller's trusted Authority and opens one Access evaluation; article bodies
// live in PostgreSQL and clients are purely HTTP.
type recallyHandlers struct {
	svc *recally.Service
	log *slog.Logger
}

func newRecallyHandlersWithService(svc *recally.Service, log *slog.Logger) *recallyHandlers {
	return &recallyHandlers{svc: svc, log: log.With("component", "recally-api")}
}

func (h *recallyHandlers) writeInternalError(w http.ResponseWriter, err error) {
	writeLoggedError(w, h.log, http.StatusInternalServerError, "internal error", err)
}

func (h *recallyHandlers) writeBadGatewayError(w http.ResponseWriter, err error) {
	writeLoggedError(w, h.log, http.StatusBadGateway, "upstream service error", err)
}

// access derives the trusted Authority for the authenticated caller and opens one
// recally Access evaluation. The Service is the sole PEP; the handler never
// inspects identity beyond deriving the Authority from verified session claims.
func (h *recallyHandlers) access(w http.ResponseWriter, r *http.Request) (*recally.Access, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	acc, err := h.svc.Begin(r.Context(), authority)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return acc, true
}

// writeRecallyError maps a recally Access error to a status. A policy denial is
// opaque (authz.ErrNotFound / ErrForbidden both surface as 404); everything else
// is a 500.
func (h *recallyHandlers) writeRecallyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusNotFound, "not found")
	default:
		h.writeInternalError(w, err)
	}
}

// ----------------------------- articles -------------------------------------

func (h *recallyHandlers) ListArticles(w http.ResponseWriter, r *http.Request, params apiserver.ListArticlesParams) {
	acc, ok := h.access(w, r)
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
		article, err := acc.GetArticleByCanonicalURL(ctx, *params.CanonicalUrl)
		if err != nil {
			writeData(w, http.StatusOK, apiserver.ArticleList{Articles: []apiserver.Article{}})
			return
		}
		writeData(w, http.StatusOK, apiserver.ArticleList{Articles: []apiserver.Article{toAPIArticle(article, "")}})
		return
	}

	if params.Q != nil && *params.Q != "" {
		articles, err := acc.SearchArticles(ctx, *params.Q, limit)
		if err != nil {
			h.writeRecallyError(w, err)
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
	articles, err := acc.ListArticles(ctx, filter)
	if err != nil {
		h.writeRecallyError(w, err)
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
	acc, ok := h.access(w, r)
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

	result, err := acc.Save(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "content is required") {
			writeError(w, http.StatusBadRequest, "content is required for new articles")
			return
		}
		h.writeRecallyError(w, err)
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
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	article, err := acc.GetArticle(r.Context(), id)
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	include := ""
	if params.Include != nil {
		include = *params.Include
	}
	if includesContent(include) {
		body, err := acc.ReadArticleBody(r.Context(), article)
		if err != nil {
			h.writeRecallyError(w, err)
			return
		}
		writeData(w, http.StatusOK, toAPIArticle(article, body))
		return
	}
	writeData(w, http.StatusOK, toAPIArticle(article, ""))
}

func (h *recallyHandlers) UpdateArticle(w http.ResponseWriter, r *http.Request, id string) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	article, err := acc.GetArticle(r.Context(), id)
	if err != nil {
		h.writeRecallyError(w, err)
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
		next, err := acc.UpdateArticle(r.Context(), id, updates)
		if err != nil {
			h.writeRecallyError(w, err)
			return
		}
		updated = next
	}

	// Body lives in PostgreSQL; only rewrite it when the client sent new content.
	// Metadata-only updates leave the stored body untouched.
	if body.Content != nil {
		if err := acc.UpsertArticleContent(r.Context(), id, *body.Content); err != nil {
			h.writeRecallyError(w, err)
			return
		}
	}

	writeData(w, http.StatusOK, toAPIArticle(updated, ""))
}

func (h *recallyHandlers) DeleteArticle(w http.ResponseWriter, r *http.Request, id string) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	if _, err := acc.GetArticle(r.Context(), id); err != nil {
		h.writeRecallyError(w, err)
		return
	}
	// The article row and its content row cascade-delete in the DB; there is no
	// on-disk mirror to clean up (legacy files, if any, are left inert).
	if err := acc.DeleteArticle(r.Context(), id); err != nil {
		h.writeRecallyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------- feeds --------------------------------------

func (h *recallyHandlers) ListFeeds(w http.ResponseWriter, r *http.Request, params apiserver.ListFeedsParams) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	if params.Url != nil && *params.Url != "" {
		feed, err := acc.GetFeedByURL(r.Context(), *params.Url)
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
	feeds, err := acc.ListFeeds(r.Context(), limit+1, offset)
	if err != nil {
		h.writeRecallyError(w, err)
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
	acc, ok := h.access(w, r)
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
	// Pre-validate input the same way the store path used to, so the 409/400
	// distinctions keep their exact messages before the (parsing) Access call.
	if existing, _ := acc.GetFeedByURL(r.Context(), body.Url); existing != nil {
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

	feed, err := acc.CreateFeed(r.Context(), body.Url, kind, strDeref(body.Title), body.AgentId)
	if err != nil {
		switch {
		case errors.Is(err, recally.ErrFeedExists):
			writeError(w, http.StatusConflict, "feed already subscribed")
		case errors.Is(err, recally.ErrFeedFetch):
			h.writeBadGatewayError(w, err)
		default:
			h.writeRecallyError(w, err)
		}
		return
	}
	w.Header().Set("Location", "/api/recally/feeds/"+feed.ID)
	writeData(w, http.StatusCreated, toAPIFeed(feed))
}

func (h *recallyHandlers) GetFeed(w http.ResponseWriter, r *http.Request, id string) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	feed, err := acc.GetFeed(r.Context(), id)
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIFeed(feed))
}

func (h *recallyHandlers) UpdateFeed(w http.ResponseWriter, r *http.Request, id string) {
	acc, ok := h.access(w, r)
	if !ok {
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
	updated, err := acc.UpdateFeed(r.Context(), id, updates)
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIFeed(updated))
}

func (h *recallyHandlers) DeleteFeed(w http.ResponseWriter, r *http.Request, id string) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	if err := acc.DeleteFeed(r.Context(), id); err != nil {
		h.writeRecallyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *recallyHandlers) PollFeed(w http.ResponseWriter, r *http.Request, id string, params apiserver.PollFeedParams) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	// Access.PollFeed loads the feed (404 if missing/foreign), skips disabled and
	// non-RSS kinds, and folds any upstream fetch failure into result.Errors — so
	// the poll stays a 200 with the error surfaced in the body, as before.
	res, err := acc.PollFeed(r.Context(), id, limit)
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	result := apiserver.FeedPollResult{Feed: toAPIFeed(&res.Feed), NewEntries: []apiserver.FeedEntry{}}
	for i := range res.NewEntries {
		result.NewEntries = append(result.NewEntries, toAPIFeedEntry(&res.NewEntries[i]))
	}
	if len(res.Errors) > 0 {
		msg := "failed to fetch feed"
		result.Error = &msg
	}
	writeData(w, http.StatusOK, result)
}

// ---------------------------- feed entries ----------------------------------

func (h *recallyHandlers) ListFeedEntries(w http.ResponseWriter, r *http.Request, feedId string, params apiserver.ListFeedEntriesParams) {
	acc, ok := h.access(w, r)
	if !ok {
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
	entries, err := acc.ListFeedEntries(r.Context(), feedId, recally.FeedEntryFilter{Status: status, Limit: limit + 1, Offset: offset})
	if err != nil {
		h.writeRecallyError(w, err)
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
	acc, ok := h.access(w, r)
	if !ok {
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
	entry, created, err := acc.CreateFeedEntry(r.Context(), feedId, body.Guid, strDeref(body.Url), strDeref(body.Title))
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	result := apitypes.CreateFeedEntryResult{Created: created}
	if entry != nil {
		apiEntry := toAPIFeedEntry(entry)
		result.Entry = &apiEntry
	}
	writeData(w, http.StatusOK, result)
}

func (h *recallyHandlers) UpdateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string, id string) {
	acc, ok := h.access(w, r)
	if !ok {
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
	updated, err := acc.UpdateFeedEntry(r.Context(), feedId, id, status, body.ArticleId, strDeref(body.ErrorMsg))
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIFeedEntry(updated))
}

// ------------------------------- digest -------------------------------------

func (h *recallyHandlers) GetDigest(w http.ResponseWriter, r *http.Request) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	digest, err := acc.GetDigest(r.Context())
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIDigest(digest))
}

func (h *recallyHandlers) ListStoredDigests(w http.ResponseWriter, r *http.Request, params apiserver.ListStoredDigestsParams) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	limitInt, offsetInt, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	limit, offset := int64(limitInt), int64(offsetInt)
	summaries, total, err := acc.ListStoredDigests(r.Context(), limit, offset)
	if err != nil {
		h.writeRecallyError(w, err)
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
	acc, ok := h.access(w, r)
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
	stored, err := acc.SaveDigest(r.Context(), body.Narrative, date)
	if err != nil {
		h.writeRecallyError(w, err)
		return
	}
	w.Header().Set("Location", "/api/recally/digests/"+stored.Date)
	writeData(w, http.StatusCreated, toAPIStoredDigest(stored))
}

func (h *recallyHandlers) GetStoredDigest(w http.ResponseWriter, r *http.Request, date string) {
	acc, ok := h.access(w, r)
	if !ok {
		return
	}
	stored, err := acc.GetStoredDigest(r.Context(), date)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			writeError(w, http.StatusNotFound, "digest not found")
		} else {
			h.writeRecallyError(w, err)
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
