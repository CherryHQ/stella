package recally

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
)

// minimalRSS is a valid RSS 2.0 document with two items, served over httptest so
// the poll path never touches the real network.
const minimalRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>https://example.com</link>
    <description>A test feed</description>
    <item><title>Item One</title><link>https://example.com/1</link><guid>guid-1</guid></item>
    <item><title>Item Two</title><link>https://example.com/2</link><guid>guid-2</guid></item>
  </channel>
</rss>`

// rssServer serves minimalRSS on every path; closed automatically via t.Cleanup.
func rssServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(minimalRSS))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// failingServer always returns 500 so gofeed surfaces a fetch error.
func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newPollService(t *testing.T, db *pgxpool.Pool) *Service {
	t.Helper()
	home := t.TempDir()
	return NewService(NewStore(db), NewFileManager(home), home)
}

func TestServicePollFeedsFiltersDisabledAndNonRSS(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := newPollService(t, db)
	store := svc.Store()
	ctx := t.Context()
	ident := authz.Identity{UserID: testUserID}

	rss := rssServer(t)

	enabled, err := store.CreateFeed(ctx, testUserID, rss.URL+"/enabled", FeedKindRSS, nil, "Enabled RSS", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed enabled: %v", err)
	}
	disabled, err := store.CreateFeed(ctx, testUserID, rss.URL+"/disabled", FeedKindRSS, nil, "Disabled RSS", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed disabled: %v", err)
	}
	if _, err := store.UpdateFeed(ctx, testUserID, disabled.ID, map[string]any{"enabled": false}); err != nil {
		t.Fatalf("UpdateFeed disabled: %v", err)
	}
	if _, err := store.CreateFeed(ctx, testUserID, "https://x.com/someone", FeedKindTwitter, nil, "A Twitter feed", "", nil); err != nil {
		t.Fatalf("CreateFeed twitter: %v", err)
	}

	results, err := svc.As(ident).PollFeeds(ctx, 20)
	if err != nil {
		t.Fatalf("PollFeeds: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("PollFeeds returned %d results, want only the enabled RSS feed", len(results))
	}
	got := results[0]
	if got.Feed.ID != enabled.ID {
		t.Fatalf("polled feed %s, want enabled feed %s", got.Feed.ID, enabled.ID)
	}
	if len(got.NewEntries) != 2 {
		t.Fatalf("NewEntries=%d, want 2", len(got.NewEntries))
	}
	if len(got.Errors) != 0 {
		t.Fatalf("Errors=%v, want none", got.Errors)
	}
}

func TestServicePollFeedsErrorIsolation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := newPollService(t, db)
	store := svc.Store()
	ctx := t.Context()
	ident := authz.Identity{UserID: testUserID}

	healthy := rssServer(t)
	broken := failingServer(t)

	okFeed, err := store.CreateFeed(ctx, testUserID, healthy.URL, FeedKindRSS, nil, "Healthy", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed healthy: %v", err)
	}
	badFeed, err := store.CreateFeed(ctx, testUserID, broken.URL, FeedKindRSS, nil, "Broken", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed broken: %v", err)
	}

	results, err := svc.As(ident).PollFeeds(ctx, 20)
	if err != nil {
		t.Fatalf("PollFeeds must not fail even when one feed errors: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	byID := map[string]FeedPollResult{}
	for _, r := range results {
		byID[r.Feed.ID] = r
	}
	if ok := byID[okFeed.ID]; len(ok.NewEntries) != 2 || len(ok.Errors) != 0 {
		t.Fatalf("healthy feed: entries=%d errors=%v, want 2 entries and no errors", len(ok.NewEntries), ok.Errors)
	}
	if bad := byID[badFeed.ID]; len(bad.Errors) == 0 || len(bad.NewEntries) != 0 {
		t.Fatalf("broken feed: entries=%d errors=%v, want 0 entries and an error", len(bad.NewEntries), bad.Errors)
	}
}

func TestServicePollFeedByIDAndOwnership(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user (id, email) VALUES ($1, 'other@example.com')`, otherUserID); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	svc := newPollService(t, db)
	store := svc.Store()
	ctx := t.Context()
	ident := authz.Identity{UserID: testUserID}

	rss := rssServer(t)

	feed, err := store.CreateFeed(ctx, testUserID, rss.URL, FeedKindRSS, nil, "Mine", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed mine: %v", err)
	}
	res, err := svc.As(ident).PollFeed(ctx, feed.ID, 20)
	if err != nil {
		t.Fatalf("PollFeed own feed: %v", err)
	}
	if len(res.NewEntries) != 2 {
		t.Fatalf("NewEntries=%d, want 2", len(res.NewEntries))
	}

	// A feed owned by another user must be invisible: not-found, never polled.
	otherFeed, err := store.CreateFeed(ctx, otherUserID, rss.URL+"/other", FeedKindRSS, nil, "Theirs", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed other: %v", err)
	}
	if _, err := svc.As(ident).PollFeed(ctx, otherFeed.ID, 20); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("PollFeed cross-user err=%v, want ErrNotFound", err)
	}
}

func TestServiceSaveDigest(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := newPollService(t, db)
	store := svc.Store()
	ctx := t.Context()
	ident := authz.Identity{UserID: testUserID}

	if _, err := svc.As(ident).SaveDigest(ctx, "", ""); err == nil || !strings.Contains(err.Error(), "narrative is required") {
		t.Fatalf("SaveDigest empty narrative err=%v, want narrative required", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	first, err := svc.As(ident).SaveDigest(ctx, "first narrative", "")
	if err != nil {
		t.Fatalf("SaveDigest first: %v", err)
	}
	if first.Date != today {
		t.Fatalf("SaveDigest defaulted date=%q, want today %q", first.Date, today)
	}
	if first.Narrative != "first narrative" {
		t.Fatalf("Narrative=%q, want %q", first.Narrative, "first narrative")
	}

	// Saving again for the same day replaces the narrative (upsert-per-day),
	// never duplicates.
	second, err := svc.As(ident).SaveDigest(ctx, "second narrative", today)
	if err != nil {
		t.Fatalf("SaveDigest second: %v", err)
	}
	if second.Narrative != "second narrative" || second.Date != today {
		t.Fatalf("second digest=%+v, want narrative replaced for today", second)
	}
	stored, err := store.GetStoredDigestByDate(ctx, testUserID, today)
	if err != nil {
		t.Fatalf("GetStoredDigestByDate: %v", err)
	}
	if stored.Narrative != "second narrative" {
		t.Fatalf("stored narrative=%q, want the replacement", stored.Narrative)
	}
	_, total, err := store.ListStoredDigests(ctx, testUserID, 10, 0)
	if err != nil {
		t.Fatalf("ListStoredDigests: %v", err)
	}
	if total != 1 {
		t.Fatalf("stored digest count=%d, want 1 (upsert, not duplicate)", total)
	}
}

func TestServiceEntryOpCrossUserDenied(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user (id, email) VALUES ($1, 'other2@example.com')`, otherUserID); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	svc := newPollService(t, db)
	store := svc.Store()
	ctx := t.Context()

	feed, err := store.CreateFeed(ctx, testUserID, "https://example.com/feed.xml", FeedKindRSS, nil, "Mine", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}

	other := authz.Identity{UserID: otherUserID}
	if _, err := svc.As(other).ListFeedEntries(ctx, feed.ID, FeedEntryFilter{}); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("ListFeedEntries cross-user err=%v, want ErrNotFound", err)
	}
}
