// Package recally provides the internal reading assistant library for Stella.
package recally

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SourceType represents the type of content source.
type SourceType string

const (
	SourceTypeWeb     SourceType = "web"
	SourceTypeTwitter SourceType = "twitter"
	SourceTypeYouTube SourceType = "youtube"
	SourceTypeGitHub  SourceType = "github"
	SourceTypeRSS     SourceType = "rss"
	SourceTypePDF     SourceType = "pdf"
)

// Valid reports whether s is a known source type.
func (s SourceType) Valid() bool {
	switch s {
	case SourceTypeWeb, SourceTypeTwitter, SourceTypeYouTube, SourceTypeGitHub, SourceTypeRSS, SourceTypePDF:
		return true
	default:
		return false
	}
}

// ArticleStatus represents the reading status of an article.
type ArticleStatus string

const (
	StatusUnread   ArticleStatus = "unread"
	StatusRead     ArticleStatus = "read"
	StatusArchived ArticleStatus = "archived"
)

// Valid reports whether s is a known article status.
func (s ArticleStatus) Valid() bool {
	switch s {
	case StatusUnread, StatusRead, StatusArchived:
		return true
	default:
		return false
	}
}

// RSSEntryStatus represents the processing status of an RSS feed entry.
type RSSEntryStatus string

const (
	EntryStatusPending RSSEntryStatus = "pending"
	EntryStatusSaved   RSSEntryStatus = "saved"
	EntryStatusSkipped RSSEntryStatus = "skipped"
	EntryStatusError   RSSEntryStatus = "error"
)

// FeedKind is the extraction-dispatch hint for a feed subscription. Go stores
// it and routes scheduling on it, but does not branch business logic on it —
// per-source item discovery lives in the recally skill.
type FeedKind string

const (
	FeedKindRSS     FeedKind = "rss"
	FeedKindTwitter FeedKind = "twitter"
	FeedKindWebsite FeedKind = "website"
)

// Valid reports whether k is a known feed kind.
func (k FeedKind) Valid() bool {
	switch k {
	case FeedKindRSS, FeedKindTwitter, FeedKindWebsite:
		return true
	default:
		return false
	}
}

// isTwitterHost reports whether host is an X/Twitter web host (ignoring a www. prefix).
func isTwitterHost(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	switch host {
	case "x.com", "twitter.com", "mobile.twitter.com", "mobile.x.com":
		return true
	default:
		return false
	}
}

// twitterReservedPaths are first path segments on X/Twitter that are not
// subscribable profile handles (system pages, search, lists, etc.).
var twitterReservedPaths = map[string]bool{
	"i":             true,
	"home":          true,
	"explore":       true,
	"search":        true,
	"hashtag":       true,
	"messages":      true,
	"notifications": true,
	"settings":      true,
	"compose":       true,
	"intent":        true,
}

// twitterHandle returns the profile handle of an X/Twitter timeline URL. It
// succeeds only when the path is a single handle segment (/<handle>), rejecting
// system pages, search, lists, individual statuses, replies, and bookmarks —
// none of which are pollable as a profile timeline.
func twitterHandle(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !isTwitterHost(u.Hostname()) {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(segs) != 1 || segs[0] == "" {
		return "", false
	}
	handle := strings.TrimPrefix(segs[0], "@")
	if handle == "" || twitterReservedPaths[strings.ToLower(handle)] {
		return "", false
	}
	for _, r := range handle {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "", false
		}
	}
	return handle, true
}

// SniffFeedKind infers a feed kind from a subscription URL. x.com / twitter.com
// hosts map to twitter; everything else defaults to rss. The caller may override
// the result with an explicit kind. URL shape is enforced separately by
// ValidateFeedSubscription.
func SniffFeedKind(rawURL string) FeedKind {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return FeedKindRSS
	}
	if isTwitterHost(u.Hostname()) {
		return FeedKindTwitter
	}
	return FeedKindRSS
}

// ValidateFeedSubscription reports whether rawURL can be subscribed as kind.
// X/Twitter hosts are only subscribable as profile timelines (/<handle>) — they
// never serve RSS, so lists, search, individual posts, and bookmarks are
// rejected regardless of kind. RSS feeds are validated when fetched, so any
// non-X URL is accepted here.
func ValidateFeedSubscription(rawURL string, kind FeedKind) error {
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && isTwitterHost(u.Hostname()) {
		if _, ok := twitterHandle(rawURL); !ok {
			return fmt.Errorf("x/twitter feeds must be a profile timeline like https://x.com/<handle>; lists, search, individual posts, and bookmarks are not subscribable")
		}
		return nil
	}
	if kind == FeedKindTwitter {
		return fmt.Errorf("kind=twitter requires an x.com/<handle> profile URL")
	}
	return nil
}

// Article represents a saved article with its metadata.
type Article struct {
	ID           string            `json:"id"`
	UserID       string            `json:"user_id"`
	AgentID      *string           `json:"agent_id,omitempty"`
	URL          string            `json:"url"`
	CanonicalURL string            `json:"canonical_url"`
	SourceType   SourceType        `json:"source_type"`
	Title        string            `json:"title"`
	Author       string            `json:"author"`
	Summary      string            `json:"summary"`
	Tags         []string          `json:"tags"`
	Status       ArticleStatus     `json:"status"`
	Starred      bool              `json:"starred"`
	FilePath     string            `json:"file_path"`
	Metadata     map[string]string `json:"metadata"`
	PublishedAt  *time.Time        `json:"published_at,omitempty"`
	SavedAt      time.Time         `json:"saved_at"`
	ReadAt       *time.Time        `json:"read_at,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	IsNew        bool              `json:"is_new"`
}

// Feed represents an RSS feed subscription.
type Feed struct {
	ID            string            `json:"id"`
	UserID        string            `json:"user_id"`
	AgentID       *string           `json:"agent_id,omitempty"`
	URL           string            `json:"url"`
	Kind          FeedKind          `json:"kind"`
	Metadata      map[string]string `json:"metadata"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	CheckInterval string            `json:"check_interval"`
	LastCheckedAt *time.Time        `json:"last_checked_at,omitempty"`
	LastETag      string            `json:"last_etag"`
	LastModified  string            `json:"last_modified"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// FeedEntry represents an entry/item from an RSS feed.
type FeedEntry struct {
	ID           string         `json:"id"`
	FeedID       string         `json:"feed_id"`
	GUID         string         `json:"guid"`
	URL          string         `json:"url"`
	Title        string         `json:"title"`
	Status       RSSEntryStatus `json:"status"`
	ArticleID    *string        `json:"article_id,omitempty"`
	Attempts     int            `json:"attempts"`
	ErrorMsg     string         `json:"error_msg"`
	DiscoveredAt time.Time      `json:"discovered_at"`
	ProcessedAt  *time.Time     `json:"processed_at,omitempty"`
}

// SaveRequest contains all data needed to save an article.
type SaveRequest struct {
	URL          string            `json:"url"`
	CanonicalURL string            `json:"canonical_url,omitempty"`
	SourceType   SourceType        `json:"source_type"`
	Title        string            `json:"title"`
	Author       string            `json:"author"`
	Summary      string            `json:"summary"`
	Tags         []string          `json:"tags"`
	Content      string            `json:"content"`
	Metadata     map[string]string `json:"metadata"`
	PublishedAt  *time.Time        `json:"published_at,omitempty"`
	AgentID      *string           `json:"agent_id,omitempty"`
}

// SearchResult contains an article with its search relevance.
type SearchResult struct {
	Article
	Rank float64 `json:"rank,omitempty"`
}

// ArticleFilter provides filtering options for listing articles.
type ArticleFilter struct {
	Status     ArticleStatus
	SourceType SourceType
	Starred    *bool
	Limit      int
	Offset     int
}

// FeedEntryFilter provides filtering options for listing feed entries.
type FeedEntryFilter struct {
	Status RSSEntryStatus
	Limit  int
	Offset int
}

// ListResult contains pagination info for list operations.
type ListResult struct {
	Items   []Article `json:"items"`
	Total   int       `json:"total"`
	HasMore bool      `json:"has_more"`
}

// FeedPollResult contains the result of polling a feed.
type FeedPollResult struct {
	Feed       Feed        `json:"feed"`
	NewEntries []FeedEntry `json:"new_entries"`
	Errors     []string    `json:"errors,omitempty"`
}

// TagCount represents a tag with its frequency.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

// DigestSection identifies which section of a digest an article belongs to.
type DigestSection = string

const (
	DigestSectionSavedYesterday  DigestSection = "saved_yesterday"
	DigestSectionWorthRevisiting DigestSection = "worth_revisiting"
)

// StoredDigest is a persisted daily digest snapshot with full article objects.
type StoredDigest struct {
	ID                   string     `json:"id"`
	UserID               string     `json:"user_id"`
	Date                 string     `json:"date"`
	Narrative            string     `json:"narrative"`
	SavedYesterday       []Article  `json:"saved_yesterday"`
	SavedYesterdayCount  int64      `json:"saved_yesterday_count"`
	UnreadCount          int64      `json:"unread_count"`
	ReadCount            int64      `json:"read_count"`
	ArchivedCount        int64      `json:"archived_count"`
	StarredCount         int64      `json:"starred_count"`
	WorthRevisiting      []Article  `json:"worth_revisiting"`
	WorthRevisitingCount int64      `json:"worth_revisiting_count"`
	TopTags              []TagCount `json:"top_tags"`
	TotalArticles        int64      `json:"total_articles"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// StoredDigestSummary is a lightweight digest record for list views.
type StoredDigestSummary struct {
	ID                   string    `json:"id"`
	Date                 string    `json:"date"`
	Narrative            string    `json:"narrative"`
	SavedYesterdayCount  int64     `json:"saved_yesterday_count"`
	WorthRevisitingCount int64     `json:"worth_revisiting_count"`
	TotalArticles        int64     `json:"total_articles"`
	CreatedAt            time.Time `json:"created_at"`
}

// Digest contains daily reading statistics and summaries.
type Digest struct {
	UserID               string     `json:"user_id"`
	Date                 time.Time  `json:"date"`
	SavedYesterday       []Article  `json:"saved_yesterday"`
	SavedYesterdayCount  int        `json:"saved_yesterday_count"`
	UnreadCount          int64      `json:"unread_count"`
	ReadCount            int64      `json:"read_count"`
	ArchivedCount        int64      `json:"archived_count"`
	StarredCount         int64      `json:"starred_count"`
	WorthRevisiting      []Article  `json:"worth_revisiting"`
	WorthRevisitingCount int        `json:"worth_revisiting_count"`
	TopTags              []TagCount `json:"top_tags"`
	TotalArticles        int64      `json:"total_articles"`
}

// FromSQLCArticle populates an Article from a sqlc Article.
func (a *Article) FromSQLCArticle(sa sqlc.RecallyArticle) {
	a.ID = sa.ID
	a.UserID = sa.UserID
	if sa.AgentID.Valid {
		a.AgentID = &sa.AgentID.String
	} else {
		a.AgentID = nil
	}
	a.URL = sa.Url
	a.CanonicalURL = sa.CanonicalUrl
	a.SourceType = SourceType(sa.SourceType)
	a.Title = sa.Title
	a.Author = sa.Author
	a.Summary = sa.Summary
	a.Tags = decodeTags(sa.Tags)
	a.Status = ArticleStatus(sa.Status)
	a.Starred = sa.Starred
	a.FilePath = sa.FilePath
	a.Metadata = decodeMetadata(sa.Metadata)
	a.PublishedAt = parseNullTime(sa.PublishedAt)
	a.SavedAt = sa.SavedAt.UTC()
	a.ReadAt = parseNullTime(sa.ReadAt)
	a.CreatedAt = sa.CreatedAt.UTC()
	a.UpdatedAt = sa.UpdatedAt.UTC()
}

// FromSQLCFeed populates a Feed from a sqlc RecallyFeed.
func (f *Feed) FromSQLCFeed(sf sqlc.RecallyFeed) {
	f.ID = sf.ID
	f.UserID = sf.UserID
	if sf.AgentID.Valid {
		f.AgentID = &sf.AgentID.String
	} else {
		f.AgentID = nil
	}
	f.URL = sf.Url
	f.Kind = FeedKind(sf.Kind)
	f.Metadata = decodeMetadata(sf.Metadata)
	f.Title = sf.Title
	f.Description = sf.Description
	f.CheckInterval = sf.CheckInterval
	f.LastCheckedAt = parseNullTime(sf.LastCheckedAt)
	f.LastETag = sf.LastEtag
	f.LastModified = sf.LastModified
	f.Enabled = sf.Enabled
	f.CreatedAt = sf.CreatedAt.UTC()
	f.UpdatedAt = sf.UpdatedAt.UTC()
}

// FromSQLCFeedEntry populates a FeedEntry from a sqlc RecallyFeedEntry.
func (e *FeedEntry) FromSQLCFeedEntry(se sqlc.RecallyFeedEntry) {
	e.ID = se.ID
	e.FeedID = se.FeedID
	e.GUID = se.Guid
	e.URL = se.Url
	e.Title = se.Title
	e.Status = RSSEntryStatus(se.Status)
	if se.ArticleID.Valid {
		e.ArticleID = &se.ArticleID.String
	} else {
		e.ArticleID = nil
	}
	e.Attempts = int(se.Attempts)
	e.ErrorMsg = se.ErrorMsg
	e.DiscoveredAt = se.DiscoveredAt.UTC()
	e.ProcessedAt = parseNullTime(se.ProcessedAt)
}

func parseNullTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	buf, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(buf)
}

func decodeTags(value string) []string {
	if value == "" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(value), &tags); err != nil {
		return []string{}
	}
	if tags == nil {
		return []string{}
	}
	return tags
}

func encodeMetadata(metadata map[string]string) json.RawMessage {
	if len(metadata) == 0 {
		return json.RawMessage("{}")
	}
	buf, err := json.Marshal(metadata)
	if err != nil {
		return json.RawMessage("{}")
	}
	return buf
}

func decodeMetadata(value json.RawMessage) map[string]string {
	if len(value) == 0 {
		return map[string]string{}
	}
	metadata := make(map[string]string)
	if err := json.Unmarshal(value, &metadata); err != nil {
		return map[string]string{}
	}
	return metadata
}
