package recally

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
)

// Sentinels the HTTP layer maps to specific statuses; the tool surfaces them as
// plain text. Feed subscription conflicts (409) and upstream fetch failures (502)
// are otherwise indistinguishable from a generic 500.
var (
	ErrFeedExists = errors.New("feed already subscribed")
	ErrFeedFetch  = errors.New("failed to fetch feed")
)

// Access is one recally use case bound to one trusted Authority. Recally is a
// user-owned library shared across all of a user's agents: every store call is
// scoped to the captured userID, so a foreign user's row is simply not found
// (opaque 404) and there is nothing else to enforce. A delegated AgentActor has
// the SAME access as its delegating user (an agent shares its user's library).
// There is no policy evaluation; the acting user is the boundary. Operations on
// content and feed entries — which are keyed only by their parent id, not by user
// — prove parent ownership through a uid-scoped parent load before mutating.
type Access struct {
	svc    *Service
	userID string
}

// Access binds one recally use case to a trusted Authority. It rejects an invalid
// Authority (403) and one carrying no user (401) up front, so every method can
// assume a non-empty acting user.
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("recally service is unavailable — try again later")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	userID := string(authority.Actor().UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	return &Access{svc: s, userID: userID}, nil
}

// loadArticle loads one article scoped to the acting user. The store is
// uid-scoped, so a foreign user's row is simply not found (opaque 404).
func (a *Access) loadArticle(ctx context.Context, id string) (*Article, error) {
	article, err := a.svc.store.GetArticle(ctx, a.userID, id)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

// loadFeed loads one feed scoped to the acting user (uid-scoped, opaque 404).
func (a *Access) loadFeed(ctx context.Context, id string) (*Feed, error) {
	feed, err := a.svc.store.GetFeed(ctx, a.userID, id)
	if err != nil {
		return nil, mapMissing(err)
	}
	return feed, nil
}

// ------------------------------- articles -----------------------------------

func (a *Access) Save(ctx context.Context, req SaveRequest) (SaveResult, error) {
	article, created, err := a.svc.save(ctx, a.userID, req)
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Article: article, Created: created}, nil
}

func (a *Access) GetArticle(ctx context.Context, id string) (*Article, error) {
	return a.loadArticle(ctx, id)
}

func (a *Access) ListArticles(ctx context.Context, filter ArticleFilter) ([]Article, error) {
	return a.svc.store.ListArticles(ctx, a.userID, filter)
}

func (a *Access) SearchArticles(ctx context.Context, query string, limit int) ([]Article, error) {
	return a.svc.store.SearchArticles(ctx, a.userID, query, limit)
}

func (a *Access) GetArticleByCanonicalURL(ctx context.Context, canonicalURL string) (*Article, error) {
	article, err := a.svc.store.GetArticleByCanonicalURL(ctx, a.userID, canonicalURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (a *Access) UpdateArticle(ctx context.Context, id string, updates map[string]any) (*Article, error) {
	article, err := a.svc.store.UpdateArticle(ctx, a.userID, id, updates)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (a *Access) DeleteArticle(ctx context.Context, id string) error {
	return a.svc.store.DeleteArticle(ctx, a.userID, id)
}

// UpsertArticleContent rewrites one owner-scoped article body. The content
// table is keyed only by article id, so this boundary must prove ownership
// itself: it loads the article uid-scoped first, so a foreign article is not
// found rather than silently overwritten.
func (a *Access) UpsertArticleContent(ctx context.Context, articleID, content string) error {
	if _, err := a.svc.store.GetArticle(ctx, a.userID, articleID); err != nil {
		return mapMissing(err)
	}
	return a.svc.store.UpsertArticleContent(ctx, articleID, content)
}

// ReadArticleBody delegates to the unauthenticated Service body reader. The
// article is already loaded uid-scoped by the caller; the Service helper stays
// identity-free (the startup backfill uses it too).
func (a *Access) ReadArticleBody(ctx context.Context, article *Article) (string, error) {
	return a.svc.ReadArticleBody(ctx, article)
}

// -------------------------------- feeds -------------------------------------

func (a *Access) ListFeeds(ctx context.Context, limit, offset int) ([]Feed, error) {
	return a.svc.store.ListFeeds(ctx, a.userID, limit, offset)
}

func (a *Access) GetFeed(ctx context.Context, feedID string) (*Feed, error) {
	return a.loadFeed(ctx, feedID)
}

func (a *Access) GetFeedByURL(ctx context.Context, feedURL string) (*Feed, error) {
	feed, err := a.svc.store.GetFeedByURL(ctx, a.userID, feedURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return feed, nil
}

func (a *Access) CreateFeed(ctx context.Context, feedURL string, kind FeedKind, title string, agentID *string) (*Feed, error) {
	if feedURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if existing, _ := a.svc.store.GetFeedByURL(ctx, a.userID, feedURL); existing != nil {
		return nil, ErrFeedExists
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
		parsed, parseErr := a.svc.feeds.ParseURLWithContext(feedURL, parseCtx)
		cancel()
		if parseErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrFeedFetch, parseErr)
		}
		if title == "" {
			title = parsed.Title
		}
		description = parsed.Description
	}
	return a.svc.store.CreateFeed(ctx, a.userID, feedURL, kind, nil, title, description, agentID)
}

func (a *Access) UpdateFeed(ctx context.Context, id string, updates map[string]any) (*Feed, error) {
	if _, err := a.loadFeed(ctx, id); err != nil {
		return nil, err
	}
	updated, err := a.svc.store.UpdateFeed(ctx, a.userID, id, updates)
	if err != nil {
		return nil, mapMissing(err)
	}
	return updated, nil
}

func (a *Access) DeleteFeed(ctx context.Context, id string) error {
	if _, err := a.loadFeed(ctx, id); err != nil {
		return err
	}
	if err := a.svc.store.DeleteFeed(ctx, a.userID, id); err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return authz.ErrNotFound
		}
		return err
	}
	return nil
}

func (a *Access) PollFeed(ctx context.Context, id string, limit int) (FeedPollResult, error) {
	feed, err := a.loadFeed(ctx, id)
	if err != nil {
		return FeedPollResult{}, err
	}
	return a.svc.pollFeed(ctx, a.userID, *feed, limit), nil
}

func (a *Access) PollFeeds(ctx context.Context, limit int) ([]FeedPollResult, error) {
	const pageSize = 500
	results := []FeedPollResult{}
	for offset := 0; ; offset += pageSize {
		feeds, err := a.svc.store.ListFeeds(ctx, a.userID, pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, feed := range feeds {
			if !feed.Enabled || feed.Kind != FeedKindRSS {
				continue
			}
			results = append(results, a.svc.pollFeed(ctx, a.userID, feed, limit))
		}
		if len(feeds) < pageSize {
			break
		}
	}
	return results, nil
}

// ----------------------------- feed entries ---------------------------------

func (a *Access) ListFeedEntries(ctx context.Context, feedID string, filter FeedEntryFilter) ([]FeedEntry, error) {
	if _, err := a.loadFeed(ctx, feedID); err != nil {
		return nil, err
	}
	status := filter.Status
	if status == "" {
		status = EntryStatusPending
	}
	if status != EntryStatusPending {
		return nil, fmt.Errorf("only status=pending is supported")
	}
	return a.svc.store.ListPendingFeedEntries(ctx, feedID, filter.Limit, filter.Offset)
}

func (a *Access) CreateFeedEntry(ctx context.Context, feedID, guid, entryURL, title string) (*FeedEntry, bool, error) {
	if _, err := a.loadFeed(ctx, feedID); err != nil {
		return nil, false, err
	}
	if guid == "" {
		return nil, false, fmt.Errorf("guid is required")
	}
	entry, err := a.svc.store.CreateFeedEntry(ctx, feedID, guid, entryURL, title)
	if err != nil {
		return nil, false, err
	}
	return entry, entry != nil, nil
}

func (a *Access) UpdateFeedEntry(ctx context.Context, feedID, id string, status RSSEntryStatus, articleID *string, errorMsg string) (*FeedEntry, error) {
	if _, err := a.loadFeed(ctx, feedID); err != nil {
		return nil, err
	}
	if _, err := a.svc.store.GetFeedEntry(ctx, feedID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(mapMissing(err), authz.ErrNotFound) {
			return nil, authz.ErrNotFound
		}
		return nil, err
	}
	switch status {
	case EntryStatusSaved, EntryStatusSkipped, EntryStatusError, EntryStatusPending:
	default:
		return nil, fmt.Errorf("invalid status")
	}
	if status == EntryStatusSaved {
		if articleID == nil || *articleID == "" {
			return nil, fmt.Errorf("article_id required when status=saved")
		}
		// Feed entries are parent-keyed and the FK alone does not enforce that the
		// linked article belongs to the same user. Prove article ownership before
		// creating the cross-reference; foreign and missing IDs stay opaque.
		if _, err := a.loadArticle(ctx, *articleID); err != nil {
			return nil, err
		}
	}
	updated, err := a.svc.store.MarkFeedEntry(ctx, feedID, id, status, articleID, errorMsg)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// -------------------------------- digest ------------------------------------

func (a *Access) GetDigest(ctx context.Context) (*Digest, error) {
	return a.svc.store.GetDigest(ctx, a.userID)
}

func (a *Access) SaveDigest(ctx context.Context, narrative, date string) (*StoredDigest, error) {
	if narrative == "" {
		return nil, fmt.Errorf("narrative is required")
	}
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	return a.svc.store.SaveDigest(ctx, a.userID, narrative, date)
}

func (a *Access) ListStoredDigests(ctx context.Context, limit, offset int64) ([]StoredDigestSummary, int64, error) {
	return a.svc.store.ListStoredDigests(ctx, a.userID, limit, offset)
}

func (a *Access) GetStoredDigest(ctx context.Context, date string) (*StoredDigest, error) {
	stored, err := a.svc.store.GetStoredDigestByDate(ctx, a.userID, date)
	if err != nil {
		// GetStoredDigestByDate wraps pgx.ErrNoRows (message lacks "not found"), so
		// map the missing row explicitly rather than through mapMissing.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, authz.ErrNotFound
		}
		return nil, err
	}
	return stored, nil
}
