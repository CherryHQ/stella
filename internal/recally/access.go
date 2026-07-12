package recally

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// Sentinels the HTTP layer maps to specific statuses; the tool surfaces them as
// plain text. Feed subscription conflicts (409) and upstream fetch failures (502)
// are otherwise indistinguishable from a generic 500.
var (
	ErrFeedExists = errors.New("feed already subscribed")
	ErrFeedFetch  = errors.New("failed to fetch feed")
)

// Access is one recally use case bound to exactly one Authorizer evaluation. The
// recally Service is the sole policy-enforcement point for the library: transports
// and the agent tool pass a trusted authz.Authority and never a bare identity.
// Recally is user-owned and shared across all of a user's agents — a delegated
// AgentActor has the SAME access as its delegating user, so every operation is
// authorized as the acting user's own library (is_owner is always true; the
// store's uid-scoping guarantees loaded rows belong to that user). A denial is
// opaque (authz.ErrNotFound), preserving recally's 404 semantics.
type Access struct {
	svc     *Service
	eval    authz.Evaluation
	userID  string
	agentID string
}

// Begin opens exactly one evaluation for one recally use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("recally authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("recally authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, userID: string(actor.UserID()), agentID: agentID}, nil
}

// authorize decides one recally action for the acting user under this Access's
// single revision. is_owner is always true: the resource is the acting user's own
// library. A denial is opaque (ErrNotFound) — a policy-hidden library is
// indistinguishable from an empty one.
func (a *Access) authorize(action authz.Action) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	req, err := policy.RecallyRequest(action, a.userID, a.userID, policy.OwnedFacts{Owner: a.userID, Agent: a.agentID, IsOwner: true})
	if err != nil {
		return authz.ErrNotFound
	}
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("recally decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrNotFound
	}
	return nil
}

// loadArticle authorizes `action` then loads one article. The store is uid-scoped,
// so a foreign user's row is simply not found (opaque 404).
func (a *Access) loadArticle(ctx context.Context, action authz.Action, id string) (*Article, error) {
	if err := a.authorize(action); err != nil {
		return nil, err
	}
	article, err := a.svc.store.GetArticle(ctx, a.userID, id)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

// loadFeed authorizes `action` then loads one feed (uid-scoped, opaque 404).
func (a *Access) loadFeed(ctx context.Context, action authz.Action, id string) (*Feed, error) {
	if err := a.authorize(action); err != nil {
		return nil, err
	}
	feed, err := a.svc.store.GetFeed(ctx, a.userID, id)
	if err != nil {
		return nil, mapMissing(err)
	}
	return feed, nil
}

// ------------------------------- articles -----------------------------------

func (a *Access) Save(ctx context.Context, req SaveRequest) (SaveResult, error) {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return SaveResult{}, err
	}
	article, created, err := a.svc.save(ctx, a.userID, req)
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Article: article, Created: created}, nil
}

func (a *Access) GetArticle(ctx context.Context, id string) (*Article, error) {
	return a.loadArticle(ctx, authz.ActionRead, id)
}

func (a *Access) ListArticles(ctx context.Context, filter ArticleFilter) ([]Article, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	return a.svc.store.ListArticles(ctx, a.userID, filter)
}

func (a *Access) SearchArticles(ctx context.Context, query string, limit int) ([]Article, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	return a.svc.store.SearchArticles(ctx, a.userID, query, limit)
}

func (a *Access) GetArticleByCanonicalURL(ctx context.Context, canonicalURL string) (*Article, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	article, err := a.svc.store.GetArticleByCanonicalURL(ctx, a.userID, canonicalURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (a *Access) UpdateArticle(ctx context.Context, id string, updates map[string]any) (*Article, error) {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return nil, err
	}
	article, err := a.svc.store.UpdateArticle(ctx, a.userID, id, updates)
	if err != nil {
		return nil, mapMissing(err)
	}
	return article, nil
}

func (a *Access) DeleteArticle(ctx context.Context, id string) error {
	if err := a.authorize(authz.ActionDelete); err != nil {
		return err
	}
	return a.svc.store.DeleteArticle(ctx, a.userID, id)
}

// UpsertArticleContent rewrites one article's body. Content rows are keyed by
// article id; callers must load the article (owner-scoped) first.
func (a *Access) UpsertArticleContent(ctx context.Context, articleID, content string) error {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return err
	}
	return a.svc.store.UpsertArticleContent(ctx, articleID, content)
}

// ReadArticleBody authorizes a read then delegates to the unauthenticated Service
// body reader. The Service helper stays PEP-free (the startup backfill uses it).
func (a *Access) ReadArticleBody(ctx context.Context, article *Article) (string, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return "", err
	}
	return a.svc.ReadArticleBody(ctx, article)
}

// -------------------------------- feeds -------------------------------------

func (a *Access) ListFeeds(ctx context.Context, limit, offset int) ([]Feed, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	return a.svc.store.ListFeeds(ctx, a.userID, limit, offset)
}

func (a *Access) GetFeed(ctx context.Context, feedID string) (*Feed, error) {
	return a.loadFeed(ctx, authz.ActionRead, feedID)
}

func (a *Access) GetFeedByURL(ctx context.Context, feedURL string) (*Feed, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	feed, err := a.svc.store.GetFeedByURL(ctx, a.userID, feedURL)
	if err != nil {
		return nil, mapMissing(err)
	}
	return feed, nil
}

func (a *Access) CreateFeed(ctx context.Context, feedURL string, kind FeedKind, title string, agentID *string) (*Feed, error) {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return nil, err
	}
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
	if _, err := a.loadFeed(ctx, authz.ActionWrite, id); err != nil {
		return nil, err
	}
	updated, err := a.svc.store.UpdateFeed(ctx, a.userID, id, updates)
	if err != nil {
		return nil, mapMissing(err)
	}
	return updated, nil
}

func (a *Access) DeleteFeed(ctx context.Context, id string) error {
	if _, err := a.loadFeed(ctx, authz.ActionDelete, id); err != nil {
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
	feed, err := a.loadFeed(ctx, authz.ActionExecute, id)
	if err != nil {
		return FeedPollResult{}, err
	}
	return a.svc.pollFeed(ctx, a.userID, *feed, limit), nil
}

func (a *Access) PollFeeds(ctx context.Context, limit int) ([]FeedPollResult, error) {
	if err := a.authorize(authz.ActionExecute); err != nil {
		return nil, err
	}
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
	if _, err := a.loadFeed(ctx, authz.ActionRead, feedID); err != nil {
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
	if _, err := a.loadFeed(ctx, authz.ActionWrite, feedID); err != nil {
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
	if _, err := a.loadFeed(ctx, authz.ActionWrite, feedID); err != nil {
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
	if status == EntryStatusSaved && (articleID == nil || *articleID == "") {
		return nil, fmt.Errorf("article_id required when status=saved")
	}
	updated, err := a.svc.store.MarkFeedEntry(ctx, feedID, id, status, articleID, errorMsg)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// -------------------------------- digest ------------------------------------

func (a *Access) GetDigest(ctx context.Context) (*Digest, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	return a.svc.store.GetDigest(ctx, a.userID)
}

func (a *Access) SaveDigest(ctx context.Context, narrative, date string) (*StoredDigest, error) {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return nil, err
	}
	if narrative == "" {
		return nil, fmt.Errorf("narrative is required")
	}
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	return a.svc.store.SaveDigest(ctx, a.userID, narrative, date)
}

func (a *Access) ListStoredDigests(ctx context.Context, limit, offset int64) ([]StoredDigestSummary, int64, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, 0, err
	}
	return a.svc.store.ListStoredDigests(ctx, a.userID, limit, offset)
}

func (a *Access) GetStoredDigest(ctx context.Context, date string) (*StoredDigest, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
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
