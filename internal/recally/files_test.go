package recally

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mirror writer is gone; the read path only has to strip YAML frontmatter
// from legacy on-disk files (for the startup backfill and the read fallback).
func TestFileManager_ReadArticleStripsFrontmatter(t *testing.T) {
	tempDir := t.TempDir()
	fm := NewFileManager(tempDir)

	path := filepath.Join(tempDir, "legacy.md")
	raw := "---\nid: test123\ntitle: Test Article\n---\n# Test Article\n\nThis is the article content.\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	body, err := fm.ReadArticle(path)
	if err != nil {
		t.Fatalf("ReadArticle failed: %v", err)
	}
	if strings.Contains(body, "id: test123") {
		t.Errorf("ReadArticle should strip frontmatter, got: %s", body)
	}
	if !strings.Contains(body, "# Test Article") {
		t.Errorf("ReadArticle missing body text: %s", body)
	}

	full, err := fm.ReadArticleFull(path)
	if err != nil {
		t.Fatalf("ReadArticleFull failed: %v", err)
	}
	if !strings.HasPrefix(full, "---") {
		t.Error("ReadArticleFull should preserve the frontmatter delimiter")
	}
	if !strings.Contains(full, "title: Test Article") {
		t.Error("ReadArticleFull should preserve frontmatter")
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
	if err := fm.EnsureLibrary("1"); err != nil {
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

	files, err := fm.ListArticles("1")
	if err != nil {
		t.Fatalf("ListArticles failed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 .md files, got %d", len(files))
	}
}
