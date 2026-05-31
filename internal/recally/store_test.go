package recally

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	// Create temp directory for test database
	tempDir, err := os.MkdirTemp("", "recally-store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Fatalf("Failed to clean up temp dir after open error: %v", removeErr)
		}
		t.Fatalf("Failed to open test database: %v", err)
	}

	// Create tables
	schema := `
CREATE TABLE IF NOT EXISTS auth_users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    is_active INTEGER NOT NULL DEFAULT 1,
    age_public_key TEXT NOT NULL DEFAULT '',
    age_private_key TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS recally_article (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    agent_id TEXT,
    url TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'web',
    title TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'unread',
    starred INTEGER NOT NULL DEFAULT 0,
    file_path TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}',
    published_at TEXT,
    saved_at TEXT NOT NULL DEFAULT (datetime('now')),
    read_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recally_article_user_canonical ON recally_article (user_id, canonical_url);

CREATE TABLE IF NOT EXISTS recally_rss_feed (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    agent_id TEXT,
    url TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    check_interval TEXT NOT NULL DEFAULT '1h',
    last_checked_at TEXT,
    last_etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recally_rss_feed_user_url ON recally_rss_feed (user_id, url);

CREATE TABLE IF NOT EXISTS recally_rss_feed_entry (
    id TEXT PRIMARY KEY,
    feed_id TEXT NOT NULL,
    guid TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    article_id TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT NOT NULL DEFAULT '',
    discovered_at TEXT NOT NULL DEFAULT (datetime('now')),
    processed_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recally_rss_feed_entry_feed_guid ON recally_rss_feed_entry (feed_id, guid);
`

	if _, err := db.Exec(schema); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("Failed to close test database: %v", closeErr)
		}
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Fatalf("Failed to remove temp dir: %v", removeErr)
		}
		t.Fatalf("Failed to create schema: %v", err)
	}

	// Insert a test user
	if _, err := db.Exec(`INSERT INTO auth_users (id, username, password_hash) VALUES (1, 'testuser', 'hash')`); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("Failed to close test database: %v", closeErr)
		}
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Fatalf("Failed to remove temp dir: %v", removeErr)
		}
		t.Fatalf("Failed to insert test user: %v", err)
	}

	cleanup := func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Failed to close test database: %v", err)
		}
		if err := os.RemoveAll(tempDir); err != nil {
			t.Fatalf("Failed to remove temp dir: %v", err)
		}
	}

	return db, cleanup
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
	article, isNew, err := store.SaveArticle(ctx, "1", req)
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
	article2, isNew2, err := store.SaveArticle(ctx, "1", req)
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
	created, _, err := store.SaveArticle(ctx, "1", req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// Get the article
	article, err := store.GetArticle(ctx, "1", created.ID)
	if err != nil {
		t.Fatalf("GetArticle failed: %v", err)
	}
	if article.Title != "Get Test" {
		t.Errorf("Expected title 'Get Test', got %q", article.Title)
	}

	// Get non-existent article
	_, err = store.GetArticle(ctx, "1", "nonexistent")
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
	created, _, err := store.SaveArticle(ctx, "1", req)
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

	updated, err := store.UpdateArticle(ctx, "1", created.ID, updates)
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
	created, _, err := store.SaveArticle(ctx, "1", req)
	if err != nil {
		t.Fatalf("SaveArticle failed: %v", err)
	}

	// Delete article
	err = store.DeleteArticle(ctx, "1", created.ID)
	if err != nil {
		t.Fatalf("DeleteArticle failed: %v", err)
	}

	// Verify deletion
	_, err = store.GetArticle(ctx, "1", created.ID)
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
		_, _, err := store.SaveArticle(ctx, "1", req)
		if err != nil {
			t.Fatalf("SaveArticle failed: %v", err)
		}
	}

	// List all articles
	filter := ArticleFilter{Limit: 10}
	articles, err := store.ListArticles(ctx, "1", filter)
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
		_, _, err := store.SaveArticle(ctx, "1", req)
		if err != nil {
			t.Fatalf("SaveArticle failed: %v", err)
		}
	}

	// Search for "Go"
	results, err := store.SearchArticles(ctx, "1", "Go", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) < 1 {
		t.Error("Expected at least one result for 'Go' search")
	}

	// Search for "Python"
	results, err = store.SearchArticles(ctx, "1", "Python", 10)
	if err != nil {
		t.Fatalf("SearchArticles failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'Python', got %d", len(results))
	}
}

func TestStore_Feeds(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := t.Context()

	// Create feed
	feed, err := store.CreateFeed(ctx, "1", "https://example.com/feed.xml", "Test Feed", "A test feed", nil)
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
	retrieved, err := store.GetFeed(ctx, "1", feed.ID)
	if err != nil {
		t.Fatalf("GetFeed failed: %v", err)
	}
	if retrieved.Title != "Test Feed" {
		t.Errorf("Expected title 'Test Feed', got %q", retrieved.Title)
	}

	// Get feed by URL
	byURL, err := store.GetFeedByURL(ctx, "1", "https://example.com/feed.xml")
	if err != nil {
		t.Fatalf("GetFeedByURL failed: %v", err)
	}
	if byURL.ID != feed.ID {
		t.Error("GetFeedByURL should return same feed")
	}

	// List feeds
	feeds, err := store.ListFeeds(ctx, "1", 50, 0)
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
	updated, err := store.UpdateFeed(ctx, "1", feed.ID, updates)
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
	err = store.DeleteFeed(ctx, "1", feed.ID)
	if err != nil {
		t.Fatalf("DeleteFeed failed: %v", err)
	}

	// Verify deletion
	_, err = store.GetFeed(ctx, "1", feed.ID)
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
	feed, err := store.CreateFeed(ctx, "1", "https://example.com/feed.xml", "Test Feed", "", nil)
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

	// Mark entry as saved
	articleID := "article-123"
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
