package recally

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileManager_ArticlePath(t *testing.T) {
	fm := NewFileManager("/tmp/anna")

	tests := []struct {
		name      string
		userID    int64
		title     string
		savedAt   time.Time
		articleID string
		want      string
	}{
		{
			name:      "basic path",
			userID:    1,
			articleID: "01HX3Q9M8N7P6Q5R4S3T2V1W0X",
			title:     "Test Article",
			savedAt:   time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
			want:      "/tmp/anna/library/1/articles/2026/04/29-test-article-01HX3Q9M8N7P.md",
		},
		{
			name:      "title with special chars",
			userID:    42,
			articleID: "shortid",
			title:     "What's New?!",
			savedAt:   time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			want:      "/tmp/anna/library/42/articles/2026/01/15-what-s-new-shortid.md",
		},
		{
			name:      "empty title",
			userID:    1,
			articleID: "01HX3Q9M8N7P6Q5R4S3T2V1W0X",
			title:     "",
			savedAt:   time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
			want:      "/tmp/anna/library/1/articles/2026/04/29-untitled-01HX3Q9M8N7P.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.ArticlePath(tt.userID, tt.articleID, tt.title, tt.savedAt)
			if got != tt.want {
				t.Errorf("ArticlePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileManager_WriteAndReadArticle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recally-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Fatalf("Failed to remove temp dir: %v", err)
		}
	})

	fm := NewFileManager(tempDir)
	article := &Article{
		ID:           "test123",
		URL:          "https://example.com/article",
		CanonicalURL: "https://example.com/article",
		Title:        "Test Article",
		Author:       "Test Author",
		SourceType:   SourceTypeWeb,
		Tags:         []string{"test", "article"},
		Status:       StatusUnread,
		Starred:      false,
		SavedAt:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
		Metadata:     map[string]string{"key1": "value1"},
	}

	content := "# Test Article\n\nThis is the article content."
	path := filepath.Join(tempDir, "test-article.md")
	if err := fm.WriteArticle(path, article, content); err != nil {
		t.Fatalf("WriteArticle failed: %v", err)
	}
	if !fm.ArticleExists(path) {
		t.Fatal("Article file should exist after writing")
	}

	readContent, err := fm.ReadArticle(path)
	if err != nil {
		t.Fatalf("ReadArticle failed: %v", err)
	}
	if !strings.Contains(readContent, "# Test Article") {
		t.Errorf("Read content missing expected text: %s", readContent)
	}

	fullContent, err := fm.ReadArticleFull(path)
	if err != nil {
		t.Fatalf("ReadArticleFull failed: %v", err)
	}
	if !strings.HasPrefix(fullContent, "---") {
		t.Error("Full content should start with frontmatter delimiter")
	}
	if !strings.Contains(fullContent, "id: test123") {
		t.Error("Frontmatter should contain article ID")
	}
	if !strings.Contains(fullContent, "title: Test Article") {
		t.Error("Frontmatter should contain article title")
	}
}

func TestFileManager_DeleteArticle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recally-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Fatalf("Failed to remove temp dir: %v", err)
		}
	})

	fm := NewFileManager(tempDir)
	path := filepath.Join(tempDir, "test.md")
	if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if err := fm.DeleteArticle(path); err != nil {
		t.Fatalf("DeleteArticle failed: %v", err)
	}
	if fm.ArticleExists(path) {
		t.Error("Article file should not exist after deletion")
	}
	if err := fm.DeleteArticle(filepath.Join(tempDir, "nonexistent.md")); err != nil {
		t.Errorf("DeleteArticle on non-existent file should not error: %v", err)
	}
}

func TestFileManager_EnsureLibrary(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recally-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Fatalf("Failed to remove temp dir: %v", err)
		}
	})

	fm := NewFileManager(tempDir)
	if err := fm.EnsureLibrary(1); err != nil {
		t.Fatalf("EnsureLibrary failed: %v", err)
	}

	libraryPath := filepath.Join(tempDir, "library", "1", "articles")
	info, err := os.Stat(libraryPath)
	if err != nil {
		t.Fatalf("Library path should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("Library path should be a directory")
	}
}

func TestFileManager_RelativePath(t *testing.T) {
	fm := NewFileManager("/home/user/.anna")

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "path inside anna home",
			path: "/home/user/.anna/library/1/articles/test.md",
			want: "library/1/articles/test.md",
		},
		{
			name: "path outside anna home",
			path: "/some/other/path.md",
			want: "/some/other/path.md",
		},
		{
			name: "exact anna home path",
			path: "/home/user/.anna",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.RelativePath(tt.path)
			if got != tt.want {
				t.Errorf("RelativePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileManager_ListArticles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recally-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Fatalf("Failed to remove temp dir: %v", err)
		}
	})

	fm := NewFileManager(tempDir)
	libraryPath := filepath.Join(tempDir, "library", "1", "articles", "2026", "04")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		t.Fatalf("Failed to create library path: %v", err)
	}

	testFiles := []string{
		filepath.Join(libraryPath, "29-article1.md"),
		filepath.Join(libraryPath, "28-article2.md"),
		filepath.Join(libraryPath, "not-an-md-file.txt"),
	}
	for _, f := range testFiles {
		if err := os.WriteFile(f, []byte("content"), 0o644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	files, err := fm.ListArticles(1)
	if err != nil {
		t.Fatalf("ListArticles failed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 .md files, got %d", len(files))
	}
}
