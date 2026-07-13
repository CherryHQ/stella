package recally

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/CherryHQ/stella/internal/authz"
)

// Service is the recally library. Ownership lives on Access (access.go), which
// scopes every store call to the acting user; the Service holds only identity-free
// internals (save/read/poll/backfill) that the Access and the startup backfill
// call.
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

func NewService(store *Store, stellaHome string) *Service {
	p := gofeed.NewParser()
	p.Client = &http.Client{Timeout: 30 * time.Second}
	p.RSSTranslator = &gofeed.DefaultRSSTranslator{}
	p.AtomTranslator = &gofeed.DefaultAtomTranslator{}
	p.JSONTranslator = &gofeed.DefaultJSONTranslator{}
	return &Service{store: store, files: newFileManager(stellaHome), feeds: p, stellaHome: stellaHome}
}

func (s *Service) Store() *Store { return s.store }

func mapMissing(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return authz.ErrNotFound
	}
	return err
}

// save persists an article and its body to PostgreSQL, which is the sole source
// of truth. New articles are written in one SaveArticleWithContent transaction;
// updates upsert the body. Bodies are no longer mirrored to a markdown file on
// disk (that mirror had no readers), and new rows leave file_path empty; the
// column stays as the legacy pointer the startup backfill consults for rows
// written before this change (reads no longer fall back to disk).
func (s *Service) save(ctx context.Context, userID string, req SaveRequest) (*Article, bool, error) {
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
	if existing != nil && content == "" {
		// Metadata-only update: recover the stored body from PostgreSQL only, so the
		// upsert below re-writes it rather than blanking it. A legacy file-only row
		// the backfill has not yet captured has no content row; we leave content
		// empty so the `!isNew && content != ""` guard skips the upsert and no empty
		// row is written. Writing an empty row here would make the row invisible to
		// BackfillMissingContent (its filter is file_path set + no content row) and
		// strand the legacy body forever.
		if body, ok, err := s.store.GetArticleContent(ctx, existing.ID); err != nil {
			return nil, false, err
		} else if ok {
			content = body
		}
	}
	article, isNew, err := s.store.SaveArticleWithContent(ctx, userID, req, content)
	if err != nil {
		return nil, false, err
	}
	if !isNew && content != "" {
		if err := s.store.UpsertArticleContent(ctx, article.ID, content); err != nil {
			return nil, false, err
		}
	}
	return article, isNew, nil
}

func (s *Service) ReadArticleBody(ctx context.Context, article *Article) (string, error) {
	if article == nil {
		return "", nil
	}
	// PostgreSQL is the sole source of truth for article bodies; this read never
	// touches the legacy disk mirror. An absent content row returns an empty body
	// (no-content, not an error). Accepted window: after upgrading, a legacy
	// file-only row reads empty until the startup backfill reaches it; it
	// self-heals within one backfill pass, and BackfillMissingContent -- not this
	// read path -- is the component that guarantees no legacy body is lost.
	body, ok, err := s.store.GetArticleContent(ctx, article.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
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

// pollFeed fetches one RSS feed and records any new entries. It is unauthenticated:
// the PEP (Access.PollFeed / Access.PollFeeds) authorizes and passes the caller's
// uid, and this stays a plain Service helper so the poll loop has no policy view.
func (s *Service) pollFeed(ctx context.Context, uid string, feed Feed, limit int) FeedPollResult {
	result := FeedPollResult{Feed: feed, NewEntries: []FeedEntry{}}
	if !feed.Enabled || feed.Kind != FeedKindRSS {
		return result
	}

	parseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	parsed, parseErr := s.feeds.ParseURLWithContext(feed.URL, parseCtx)
	cancel()
	if parseErr != nil {
		slog.Warn("feed poll upstream error", "feed_id", feed.ID, "url", feed.URL, "error", parseErr)
		result.Errors = []string{"failed to fetch feed: " + parseErr.Error()}
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
	} else {
		slog.Warn("failed to update feed last_checked_at", "feed_id", feed.ID, "error", err)
	}
	return result
}
