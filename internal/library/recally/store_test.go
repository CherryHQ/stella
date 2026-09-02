package recally

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// recally_*.user_id columns are PG uuid, so test users need valid uuids.
// testUserID owns every saved row; otherUserID is the scoping-test outsider.
var (
	testUserID  = uuid.NewString()
	otherUserID = uuid.NewString()
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	db := dbtest.New(t)

	// recally_article.user_id has a FK to auth_user(id); the tests save as
	// testUserID, so that row must exist before any insert.
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user (id, email) VALUES ($1, 'testuser@example.com')`, testUserID); err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	return db, func() {}
}

func TestStore_SaveArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	req := SaveRequest{
		URL:         "https://example.com/article",
		SourceType:  SourceTypeWeb,
		Title:       "Test Article",
		Author:      "Test Author",
		Summary:     "A test summary",
		Tags:        []string{"test", "article"},
		Content:     "# Content",
		Metadata:    map[string]string{"key": "value"},
		PublishedAt: &time.Time{},
	}

	// Save new article
	article, isNew, err := store.SaveArticle(ctx, testUserID, req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}
	if !isNew {
		t.Error("Expected isNew=true for new article")
	}

	if article.ID == "" {
		t.Error("Article should have an ID")
	}
	if article.Title != "Test Article" {
		t.Errorf("Expected title 'Test Article', got %q", article.Title)
	}
	if len(article.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(article.Tags))
	}

	// Save same article again - should update
	req.Title = "Updated Title"
	article2, isNew2, err := store.SaveArticle(ctx, testUserID, req)
	if err != nil {
		t.Fatalf("SaveArticle (update) failed: %v", err)
	}
	if isNew2 {
		t.Error("Expected isNew=false for existing article")
	}
	if article2.Title != "Updated Title" {
		t.Errorf("Expected updated title, got %q", article2.Title)
	}
	if article2.ID != article.ID {
		t.Error("Should update same article ID")
	}
}

func TestStore_GetArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create an article first
	req := SaveRequest{
		URL:        "https://example.com/get-test",
		SourceType: SourceTypeWeb,
		Title:      "Get Test",
	}
	created, _, err := store.SaveArticle(ctx, testUserID, req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// Get the article
	article, err := store.GetArticle(ctx, testUserID, created.ID)
	if err != nil {
		t.Fatalf("GetArticle failed: %v", err)
	}
	if article.Title != "Get Test" {
		t.Errorf("Expected title 'Get Test', got %q", article.Title)
	}

	// Get non-existent article
	_, err = store.GetArticle(ctx, testUserID, "nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent article")
	}
}

func TestStore_UpdateArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create article
	req := SaveRequest{
		URL:        "https://example.com/update-test",
		SourceType: SourceTypeWeb,
		Title:      "Original Title",
	}
	created, _, err := store.SaveArticle(ctx, testUserID, req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// Update article
	updates := map[string]any{
		"title":   "Updated Title",
		"summary": "Updated summary",
		"status":  string(StatusRead),
		"starred": true,
	}

	updated, err := store.UpdateArticle(ctx, testUserID, created.ID, updates)
	if err != nil {
		t.Fatalf("UpdateArticle failed: %v", err)
	}

	if updated.Title != "Updated Title" {
		t.Errorf("Expected title 'Updated Title', got %q", updated.Title)
	}
	if updated.Status != StatusRead {
		t.Errorf("Expected status 'read', got %q", updated.Status)
	}
	if !updated.Starred {
		t.Error("Expected starred=true")
	}
	if updated.ReadAt == nil {
		t.Error("Expected ReadAt to be set when marking as read")
	}
}

func TestStore_DeleteArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create article
	req := SaveRequest{
		URL:        "https://example.com/delete-test",
		SourceType: SourceTypeWeb,
		Title:      "Delete Test",
	}
	created, _, err := store.SaveArticle(ctx, testUserID, req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// Delete article
	err = store.DeleteArticle(ctx, testUserID, created.ID)
	if err != nil {
		t.Fatalf("DeleteArticle failed: %v", err)
	}

	// Verify deletion
	_, err = store.GetArticle(ctx, testUserID, created.ID)
	if err == nil {
		t.Error("Expected error after deleting article")
	}
}

func TestStore_ListArticles(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create multiple articles
	for i := range 5 {
		req := SaveRequest{
			URL:        fmt.Sprintf("https://example.com/list-test-%d", i),
			SourceType: SourceTypeWeb,
			Title:      fmt.Sprintf("Article %d", i),
		}
		_, _, err := store.SaveArticle(ctx, testUserID, req)
		if err != nil {
			t.Fatalf("SaveArticle failed: %v", err)
		}
	}

	// List all articles
	filter := ArticleFilter{Limit: 10}
	articles, err := store.ListArticles(ctx, testUserID, filter)
	if err != nil {
		t.Fatalf("ListArticles failed: %v", err)
	}
	if len(articles) != 5 {
		t.Errorf("Expected 5 articles, got %d", len(articles))
	}
}

func TestStore_SearchArticles(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create articles
	articles := []SaveRequest{
		{URL: "https://example.com/go-tutorial", Title: "Go Tutorial", Summary: "Learn Go"},
		{URL: "https://example.com/rust-guide", Title: "Rust Guide", Summary: "Learn Rust"},
		{URL: "https://example.com/python-tips", Title: "Python Tips", Summary: "Python tricks"},
	}

	for _, req := range articles {
		_, _, err := store.SaveArticle(ctx, testUserID, req)
		if err != nil {
			t.Fatalf("SaveArticle failed: %v", err)
		}
	}

	// Search for "Go"
	results, err := store.SearchArticles(ctx, testUserID, "Go", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) < 1 {
		t.Error("Expected at least one result for 'Go' search")
	}

	// Search for "Python"
	results, err = store.SearchArticles(ctx, testUserID, "Python", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'Python', got %d", len(results))
	}
}

func TestStore_SearchArticles_RanksTitleAboveAuthor(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Same term in a low-weight column (author) vs the high-weight title.
	for _, req := range []SaveRequest{
		{URL: "https://example.com/by-quasar", Title: "Unrelated piece", Author: "Quasar Quill"},
		{URL: "https://example.com/about-quasar", Title: "Quasar formation explained", Author: "Someone Else"},
	} {
		if _, _, err := store.SaveArticle(ctx, testUserID, req); err != nil {
			t.Fatalf("SaveArticle failed: %v", err)
		}
	}

	results, err := store.SearchArticles(ctx, testUserID, "quasar", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Quasar formation explained" {
		t.Errorf("Expected title match ranked first, got %q", results[0].Title)
	}
}

func TestStore_SearchArticles_UserScoping(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	if _, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL: "https://example.com/mine", Title: "Private zeppelin notes",
	}); err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	results, err := store.SearchArticles(ctx, otherUserID, "zeppelin", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected no results for another user, got %d", len(results))
	}
}

func TestStore_SearchArticles_CJKAndCaseInsensitive(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	if _, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL: "https://example.com/deploy", Title: "今天讨论了部署方案", Summary: "K8s 上线计划",
	}); err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// CJK is segmented by jieba, so both a multi-word and a single-word CJK query
	// match via BM25 with no fallback tier.
	results, err := store.SearchArticles(ctx, testUserID, "部署方案", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 hit for 部署方案, got %d", len(results))
	}

	results, err = store.SearchArticles(ctx, testUserID, "部署", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 hit for 部署, got %d", len(results))
	}
	results, err = store.SearchArticles(ctx, otherUserID, "部署", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected search to stay user-scoped, got %d hits", len(results))
	}

	// jieba lowercases tokens, so an uppercase query finds the "K8s" summary token.
	results, err = store.SearchArticles(ctx, testUserID, "K8S", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 case-insensitive hit for K8S, got %d", len(results))
	}
}

func TestStore_SearchArticles_NoStaleHitsAfterDeleteAndUpdate(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	deleted, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL: "https://example.com/doomed", Title: "Obsolete walrus manual",
	})
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}
	if err := store.DeleteArticle(ctx, testUserID, deleted.ID); err != nil {
		t.Fatalf("DeleteArticle failed: %v", err)
	}
	results, err := store.SearchArticles(ctx, testUserID, "walrus", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected no hits after delete, got %d", len(results))
	}

	// Updates must drop the old terms from the index and add the new ones.
	article, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL: "https://example.com/renamed", Title: "Original ocelot title",
	})
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}
	if _, err := store.UpdateArticle(ctx, testUserID, article.ID, map[string]any{"title": "Renamed capybara title"}); err != nil {
		t.Fatalf("UpdateArticle failed: %v", err)
	}
	results, err = store.SearchArticles(ctx, testUserID, "ocelot", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected no hits for replaced title, got %d", len(results))
	}
	results, err = store.SearchArticles(ctx, testUserID, "capybara", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 hit for new title, got %d", len(results))
	}
}

func TestStore_SearchArticles_SpecialCharsAndEmptyQuery(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	if _, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL: "https://example.com/special", Title: "C++ versus Rust: a (biased) take",
	}); err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// FTS5 operators in user input must never produce a query error.
	results, err := store.SearchArticles(ctx, testUserID, `rust* AND (biased) -take "c++"`, 10)
	if err != nil {
		t.Fatalf("SearchArticles with operators failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 hit, got %d", len(results))
	}

	// Queries with no extractable tokens return empty without touching FTS.
	for _, q := range []string{"", "   ", `*** (") -:`} {
		results, err := store.SearchArticles(ctx, testUserID, q, 10)
		if err != nil {
			t.Fatalf("SearchArticles(%q) failed: %v", q, err)
		}
		if len(results) != 0 {
			t.Errorf("SearchArticles(%q): expected no results, got %d", q, len(results))
		}
	}
}

func TestStore_Feeds(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create feed
	feed, err := store.CreateFeed(ctx, testUserID, "https://example.com/feed.xml", FeedKindRSS, nil, "Test Feed", "A test feed", nil)
	if err != nil {
		t.Fatalf("CreateFeed failed: %v", err)
	}
	if feed.ID == "" {
		t.Error("Feed should have an ID")
	}
	if feed.URL != "https://example.com/feed.xml" {
		t.Errorf("Expected URL 'https://example.com/feed.xml', got %q", feed.URL)
	}

	// Get feed
	retrieved, err := store.GetFeed(ctx, testUserID, feed.ID)
	if err != nil {
		t.Fatalf("GetFeed failed: %v", err)
	}
	if retrieved.Title != "Test Feed" {
		t.Errorf("Expected title 'Test Feed', got %q", retrieved.Title)
	}

	// Get feed by URL
	byURL, err := store.GetFeedByURL(ctx, testUserID, "https://example.com/feed.xml")
	if err != nil {
		t.Fatalf("GetFeedByURL failed: %v", err)
	}
	if byURL.ID != feed.ID {
		t.Error("GetFeedByURL should return same feed")
	}

	// List feeds
	feeds, err := store.ListFeeds(ctx, testUserID, 50, 0)
	if err != nil {
		t.Fatalf("ListFeeds failed: %v", err)
	}
	if len(feeds) != 1 {
		t.Errorf("Expected 1 feed, got %d", len(feeds))
	}

	// Update feed
	updates := map[string]any{
		"title":         "Updated Feed Title",
		"enabled":       false,
		"last_etag":     "\"abc123\"",
		"last_modified": "Wed, 29 Apr 2026 12:00:00 GMT",
	}
	updated, err := store.UpdateFeed(ctx, testUserID, feed.ID, updates)
	if err != nil {
		t.Fatalf("UpdateFeed failed: %v", err)
	}
	if updated.Title != "Updated Feed Title" {
		t.Errorf("Expected updated title, got %q", updated.Title)
	}
	if updated.Enabled {
		t.Error("Expected feed to be disabled")
	}

	// Delete feed
	err = store.DeleteFeed(ctx, testUserID, feed.ID)
	if err != nil {
		t.Fatalf("DeleteFeed failed: %v", err)
	}

	// Verify deletion
	_, err = store.GetFeed(ctx, testUserID, feed.ID)
	if err == nil {
		t.Error("Expected error after deleting feed")
	}
}

func TestStore_FeedEntries(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create feed
	feed, err := store.CreateFeed(ctx, testUserID, "https://example.com/feed.xml", FeedKindRSS, nil, "Test Feed", "", nil)
	if err != nil {
		t.Fatalf("CreateFeed failed: %v", err)
	}

	// Create entries
	entries := []struct {
		guid  string
		title string
		url   string
	}{
		{"guid-1", "Entry 1", "https://example.com/entry1"},
		{"guid-2", "Entry 2", "https://example.com/entry2"},
		{"guid-3", "Entry 3", "https://example.com/entry3"},
	}

	for _, e := range entries {
		_, err := store.CreateFeedEntry(ctx, feed.ID, e.guid, e.url, e.title)
		if err != nil {
			t.Fatalf("CreateFeedEntry failed: %v", err)
		}
	}

	// List pending entries
	pending, err := store.ListPendingFeedEntries(ctx, feed.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListPendingFeedEntries failed: %v", err)
	}
	if len(pending) != 3 {
		t.Errorf("Expected 3 pending entries, got %d", len(pending))
	}

	// Mark entry as saved. recally_feed_entry.article_id has a FK to
	// recally_article(id), so the referenced article must really exist.
	savedArticle, _, err := store.SaveArticle(ctx, testUserID, SaveRequest{
		URL:        "https://example.com/feed-saved",
		SourceType: SourceTypeWeb,
		Title:      "Feed Saved Article",
	})
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}
	articleID := savedArticle.ID
	updated, err := store.MarkFeedEntry(ctx, feed.ID, pending[0].ID, EntryStatusSaved, &articleID, "")
	if err != nil {
		t.Fatalf("MarkFeedEntry failed: %v", err)
	}
	if updated.Status != EntryStatusSaved {
		t.Errorf("Expected status 'saved', got %q", updated.Status)
	}
	if updated.ArticleID == nil || *updated.ArticleID != articleID {
		t.Error("Expected article ID to be set")
	}
	if updated.Attempts != 1 {
		t.Errorf("Expected attempts=1, got %d", updated.Attempts)
	}

	// Mark entry as error
	_, err = store.MarkFeedEntry(ctx, feed.ID, pending[1].ID, EntryStatusError, nil, "Failed to fetch")
	if err != nil {
		t.Fatalf("MarkFeedEntry (error) failed: %v", err)
	}

	// Check that error entry is still returned with attempts < 3
	errPending, err := store.ListPendingFeedEntries(ctx, feed.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListPendingFeedEntries failed: %v", err)
	}
	// Should have 2: 1 saved (not pending) + 1 error (still pending with < 3 attempts) + 1 untouched
	if len(errPending) != 2 {
		t.Errorf("Expected 2 pending entries (1 error + 1 untouched), got %d", len(errPending))
	}
}
