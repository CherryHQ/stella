package recally

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 500
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Save and read the user's Recally library. Actions: save batches fetched article content, list_articles, get_article, feed_add/feed_list/feed_poll/feed_remove, entry_list/entry_add/entry_update, digest/digest_save. For save, fetch the article content yourself first (for example with web/tap tools) and include markdown content for new articles; content is required for new articles. The library is shared across this user's agents.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("recally service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "recally")
	if err != nil {
		return "", err
	}
	action, err := tools.ActionArg(args, "recally")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, recallyHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", authz.MapError("recally", err)
	}
	return tools.MarshalResult(out)
}

type recallyHandler struct {
	svc   *Service
	ident authz.Identity
}

func (h recallyHandler) Save(ctx context.Context, in SaveInput) (any, error) {
	results := make([]recallySaveResult, 0, len(in.Items))
	for _, item := range in.Items {
		result := recallySaveResult{URL: item.Url}
		saved, err := h.svc.As(h.ident).Save(ctx, recallySaveRequest(item))
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.ID = saved.Article.ID
		if saved.Created {
			result.Status = "created"
		} else {
			result.Status = "updated"
		}
		results = append(results, result)
	}
	return map[string]any{"results": results}, nil
}

func (h recallyHandler) ListArticles(ctx context.Context, in ListArticlesInput) (any, error) {
	if in.Q != "" && in.PageToken != "" {
		return nil, fmt.Errorf("page_token is not supported with q")
	}
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	if in.CanonicalUrl != "" {
		article, err := h.svc.As(h.ident).GetArticleByCanonicalURL(ctx, in.CanonicalUrl)
		if err != nil {
			if errors.Is(err, authz.ErrNotFound) {
				return listResponse[recallyArticleListItem]{Items: []recallyArticleListItem{}, HasMore: false}, nil
			}
			return nil, err
		}
		return listResponse[recallyArticleListItem]{Items: []recallyArticleListItem{recallyArticleListSummary(*article)}, HasMore: false}, nil
	}
	var articles []Article
	if in.Q != "" {
		articles, err = h.svc.As(h.ident).SearchArticles(ctx, in.Q, limit)
	} else {
		articles, err = h.svc.As(h.ident).ListArticles(ctx, ArticleFilter{Status: ArticleStatus(in.Status), SourceType: SourceType(in.SourceType), Starred: in.Starred, Limit: limit + 1, Offset: offset})
	}
	if err != nil {
		return nil, err
	}
	page, next := tools.PageRows(articles, limit, offset)
	items := make([]recallyArticleListItem, 0, len(page))
	for _, article := range page {
		items = append(items, recallyArticleListSummary(article))
	}
	return listResponse[recallyArticleListItem]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h recallyHandler) GetArticle(ctx context.Context, in GetArticleInput) (any, error) {
	article, err := h.svc.As(h.ident).GetArticle(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	content, err := h.svc.ReadArticleBody(ctx, article)
	if err != nil {
		return nil, err
	}
	content, truncated := tools.TruncateText(content, 50*1024)
	return recallyArticleDetail{Article: recallyArticleListSummary(*article), Content: content, Truncated: truncated, Note: recallyTruncationNote(truncated)}, nil
}

func (h recallyHandler) FeedAdd(ctx context.Context, in FeedAddInput) (any, error) {
	feed, err := h.svc.As(h.ident).CreateFeed(ctx, in.Url, FeedKind(in.Kind), in.Title, nil)
	if err != nil {
		return nil, err
	}
	return recallyFeedSummary(*feed), nil
}

func (h recallyHandler) FeedList(ctx context.Context, in FeedListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	if in.Url != "" {
		feed, err := h.svc.As(h.ident).GetFeedByURL(ctx, in.Url)
		if err != nil {
			if errors.Is(err, authz.ErrNotFound) {
				return listResponse[recallyFeedItem]{Items: []recallyFeedItem{}, HasMore: false}, nil
			}
			return nil, err
		}
		return listResponse[recallyFeedItem]{Items: []recallyFeedItem{recallyFeedSummary(*feed)}, HasMore: false}, nil
	}
	feeds, err := h.svc.As(h.ident).ListFeeds(ctx, limit+1, offset)
	if err != nil {
		return nil, err
	}
	page, next := tools.PageRows(feeds, limit, offset)
	items := make([]recallyFeedItem, 0, len(page))
	for _, feed := range page {
		items = append(items, recallyFeedSummary(feed))
	}
	return listResponse[recallyFeedItem]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h recallyHandler) FeedPoll(ctx context.Context, in FeedPollInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = defaultToolPageSize
	}
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("invalid limit — use limit between 1 and 500")
	}
	var results []FeedPollResult
	var err error
	if in.Id == "" {
		results, err = h.svc.As(h.ident).PollFeeds(ctx, limit)
	} else {
		var result FeedPollResult
		result, err = h.svc.As(h.ident).PollFeed(ctx, in.Id, limit)
		results = []FeedPollResult{result}
	}
	if err != nil {
		return nil, err
	}
	items := make([]recallyFeedPollResult, 0, len(results))
	for _, result := range results {
		entries := make([]recallyFeedEntryItem, 0, len(result.NewEntries))
		for _, entry := range result.NewEntries {
			entries = append(entries, recallyFeedEntrySummary(entry))
		}
		out := recallyFeedPollResult{Feed: recallyFeedSummary(result.Feed), NewEntries: entries}
		if len(result.Errors) > 0 {
			out.Error = result.Errors[0]
		}
		items = append(items, out)
	}
	return map[string]any{"results": items}, nil
}

func (h recallyHandler) FeedRemove(ctx context.Context, in FeedRemoveInput) (any, error) {
	if err := h.svc.As(h.ident).DeleteFeed(ctx, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "removed"}, nil
}

func (h recallyHandler) EntryList(ctx context.Context, in EntryListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	entries, err := h.svc.As(h.ident).ListFeedEntries(ctx, in.FeedId, FeedEntryFilter{Status: RSSEntryStatus(in.Status), Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	page, next := tools.PageRows(entries, limit, offset)
	items := make([]recallyFeedEntryItem, 0, len(page))
	for _, entry := range page {
		items = append(items, recallyFeedEntrySummary(entry))
	}
	return listResponse[recallyFeedEntryItem]{Items: items, HasMore: next != ""}, nil
}

func (h recallyHandler) EntryAdd(ctx context.Context, in EntryAddInput) (any, error) {
	entry, created, err := h.svc.As(h.ident).CreateFeedEntry(ctx, in.FeedId, in.Guid, in.Url, in.Title)
	if err != nil {
		return nil, err
	}
	result := recallyCreateFeedEntryResult{Created: created}
	if entry != nil {
		item := recallyFeedEntrySummary(*entry)
		result.Entry = &item
	}
	return result, nil
}

func (h recallyHandler) EntryUpdate(ctx context.Context, in EntryUpdateInput) (any, error) {
	var articleID *string
	if in.ArticleId != "" {
		articleID = &in.ArticleId
	}
	entry, err := h.svc.As(h.ident).UpdateFeedEntry(ctx, in.FeedId, in.Id, RSSEntryStatus(in.Status), articleID, in.ErrorMsg)
	if err != nil {
		return nil, err
	}
	return recallyFeedEntrySummary(*entry), nil
}

func (h recallyHandler) Digest(ctx context.Context, _ DigestInput) (any, error) {
	digest, err := h.svc.As(h.ident).GetDigest(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"date": digest.Date.UTC().Format(time.RFC3339), "text": recallyDigestText(digest)}, nil
}

func (h recallyHandler) DigestSave(ctx context.Context, in DigestSaveInput) (any, error) {
	stored, err := h.svc.As(h.ident).SaveDigest(ctx, in.Narrative, in.Date)
	if err != nil {
		return nil, err
	}
	return map[string]any{"date": stored.Date, "saved": true}, nil
}

type recallySaveResult struct {
	URL    string `json:"url"`
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type recallyArticleListItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	SavedAt string `json:"saved_at"`
}
type recallyArticleDetail struct {
	Article   recallyArticleListItem `json:"article"`
	Content   string                 `json:"content"`
	Truncated bool                   `json:"truncated"`
	Note      string                 `json:"note,omitempty"`
}
type recallyFeedItem struct {
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Enabled   bool              `json:"enabled"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	UpdatedAt string            `json:"updated_at"`
}
type recallyFeedEntryItem struct {
	ID           string  `json:"id"`
	FeedID       string  `json:"feed_id"`
	GUID         string  `json:"guid"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	ArticleID    *string `json:"article_id,omitempty"`
	Attempts     int     `json:"attempts"`
	ErrorMsg     string  `json:"error_msg,omitempty"`
	DiscoveredAt string  `json:"discovered_at"`
	ProcessedAt  string  `json:"processed_at,omitempty"`
}
type recallyFeedPollResult struct {
	Feed       recallyFeedItem        `json:"feed"`
	NewEntries []recallyFeedEntryItem `json:"new_entries"`
	Error      string                 `json:"error,omitempty"`
}
type recallyCreateFeedEntryResult struct {
	Created bool                  `json:"created"`
	Entry   *recallyFeedEntryItem `json:"entry,omitempty"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func recallySaveRequest(item SaveItem) SaveRequest {
	return SaveRequest{URL: item.Url, CanonicalURL: item.CanonicalUrl, SourceType: SourceType(item.SourceType), Title: item.Title, Author: item.Author, Summary: item.Summary, Tags: stringItems(item.Tags), Content: item.Content, Metadata: stringMap(item.Metadata), PublishedAt: parseOptionalTime(item.PublishedAt)}
}

func recallyArticleListSummary(article Article) recallyArticleListItem {
	return recallyArticleListItem{ID: article.ID, Title: article.Title, URL: article.URL, SavedAt: article.SavedAt.UTC().Format(time.RFC3339)}
}

func recallyFeedSummary(feed Feed) recallyFeedItem {
	return recallyFeedItem{ID: feed.ID, URL: feed.URL, Kind: string(feed.Kind), Title: feed.Title, Enabled: feed.Enabled, Metadata: feed.Metadata, UpdatedAt: feed.UpdatedAt.UTC().Format(time.RFC3339)}
}

func recallyFeedEntrySummary(entry FeedEntry) recallyFeedEntryItem {
	item := recallyFeedEntryItem{ID: entry.ID, FeedID: entry.FeedID, GUID: entry.GUID, URL: entry.URL, Title: entry.Title, Status: string(entry.Status), ArticleID: entry.ArticleID, Attempts: entry.Attempts, ErrorMsg: entry.ErrorMsg, DiscoveredAt: entry.DiscoveredAt.UTC().Format(time.RFC3339)}
	if entry.ProcessedAt != nil {
		item.ProcessedAt = entry.ProcessedAt.UTC().Format(time.RFC3339)
	}
	return item
}

func recallyTruncationNote(truncated bool) string {
	if truncated {
		return "truncated — use the web UI for the full article"
	}
	return ""
}

func recallyDigestText(d *Digest) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Digest for %s\n", d.Date.UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "Total articles: %d; unread: %d; read: %d; archived: %d; starred: %d\n", d.TotalArticles, d.UnreadCount, d.ReadCount, d.ArchivedCount, d.StarredCount)
	if len(d.SavedYesterday) > 0 {
		b.WriteString("\nSaved yesterday:\n")
		for _, article := range d.SavedYesterday {
			fmt.Fprintf(&b, "- %s — %s\n", article.Title, article.URL)
		}
	}
	if len(d.WorthRevisiting) > 0 {
		b.WriteString("\nWorth revisiting:\n")
		for _, article := range d.WorthRevisiting {
			fmt.Fprintf(&b, "- %s — %s\n", article.Title, article.URL)
		}
	}
	if len(d.TopTags) > 0 {
		b.WriteString("\nTop tags:\n")
		for _, tag := range d.TopTags {
			fmt.Fprintf(&b, "- %s (%d)\n", tag.Tag, tag.Count)
		}
	}
	return b.String()
}

func stringItems(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(items map[string]any) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for k, v := range items {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func parseOptionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}
