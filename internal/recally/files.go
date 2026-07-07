// Package recally provides file operations for article storage.
package recally

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileManager reads legacy article files from disk. Bodies now live in
// PostgreSQL; the disk mirror is no longer written and the read path no longer
// consults it, so the only remaining reader is the startup backfill
// (BackfillMissingContent), which drains legacy file-only bodies into the DB.
type FileManager struct {
	stellaHome string
}

// NewFileManager creates a new FileManager instance.
func NewFileManager(stellaHome string) *FileManager {
	return &FileManager{stellaHome: stellaHome}
}

// ReadArticle reads an article file and returns the body content without frontmatter.
func (fm *FileManager) ReadArticle(path string) (string, error) {
	content, err := fm.ReadArticleFull(path)
	if err != nil {
		return "", err
	}
	_, body := SplitFrontmatter(content)
	return strings.TrimSpace(body), nil
}

// ReadArticleFull reads the entire article file including frontmatter.
func (fm *FileManager) ReadArticleFull(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(data), nil
}

func SplitFrontmatter(content string) (string, string) {
	if !strings.HasPrefix(content, "---\n") && content != "---" {
		return "", content
	}
	rest := strings.TrimPrefix(content, "---")
	before, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "", content
	}
	return strings.TrimPrefix(before, "\n"), strings.TrimPrefix(after, "\n")
}

// ArticleExists reports whether an article file exists.
func (fm *FileManager) ArticleExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// EnsureLibrary creates the library directory structure if it doesn't exist.
func (fm *FileManager) EnsureLibrary(userID string) error {
	libraryPath := filepath.Join(fm.stellaHome, "library", userID, "articles")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		return fmt.Errorf("create library: %w", err)
	}
	return nil
}

// ListArticles returns all article markdown files in a user's library.
func (fm *FileManager) ListArticles(userID string) ([]string, error) {
	libraryPath := filepath.Join(fm.stellaHome, "library", userID, "articles")
	var files []string
	err := filepath.Walk(libraryPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("walk library: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// RebuildIndex lists article files for future DB reindexing workflows.
func (fm *FileManager) RebuildIndex(userID string) ([]string, error) {
	return fm.ListArticles(userID)
}
