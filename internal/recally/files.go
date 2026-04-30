// Package recally provides file operations for article storage.
package recally

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileManager handles reading and writing article files to disk.
type FileManager struct {
	annaHome string
}

// NewFileManager creates a new FileManager instance.
func NewFileManager(annaHome string) *FileManager {
	return &FileManager{annaHome: annaHome}
}

// ArticlePath generates the storage path for an article file.
func (fm *FileManager) ArticlePath(userID int64, title string, savedAt time.Time) string {
	slug := ExtractSlug(title)
	year := savedAt.UTC().Format("2006")
	month := savedAt.UTC().Format("01")
	day := savedAt.UTC().Format("02")
	return filepath.Join(fm.annaHome, "library", fmt.Sprintf("%d", userID), "articles", year, month, fmt.Sprintf("%s-%s.md", day, slug))
}

// RelativePath returns a path relative to ANNA_HOME.
func (fm *FileManager) RelativePath(absolutePath string) string {
	rel, err := filepath.Rel(fm.annaHome, absolutePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return absolutePath
	}
	if rel == "." {
		return ""
	}
	return rel
}

// WriteArticle writes an article to disk with YAML frontmatter.
func (fm *FileManager) WriteArticle(path string, article *Article, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	frontmatter, err := fm.buildFrontmatter(article)
	if err != nil {
		return fmt.Errorf("build frontmatter: %w", err)
	}

	body := strings.TrimRight(content, "\n") + "\n"
	fullContent := frontmatter + "\n" + body
	if err := os.WriteFile(path, []byte(fullContent), 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
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

// DeleteArticle removes an article file from disk.
func (fm *FileManager) DeleteArticle(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete file: %w", err)
	}
	fm.cleanupEmptyDirs(filepath.Dir(path))
	return nil
}

func (fm *FileManager) buildFrontmatter(article *Article) (string, error) {
	frontmatter := map[string]any{
		"id":            article.ID,
		"url":           article.URL,
		"canonical_url": article.CanonicalURL,
		"title":         article.Title,
		"source_type":   article.SourceType,
		"status":        article.Status,
		"starred":       article.Starred,
	}
	if article.Author != "" {
		frontmatter["author"] = article.Author
	}
	if len(article.Tags) > 0 {
		frontmatter["tags"] = article.Tags
	}
	if !article.SavedAt.IsZero() {
		frontmatter["saved_at"] = article.SavedAt.UTC().Format(time.RFC3339)
	}
	if article.PublishedAt != nil && !article.PublishedAt.IsZero() {
		frontmatter["published_at"] = article.PublishedAt.UTC().Format(time.RFC3339)
	}
	if article.ReadAt != nil && !article.ReadAt.IsZero() {
		frontmatter["read_at"] = article.ReadAt.UTC().Format(time.RFC3339)
	}

	keys := make([]string, 0, len(article.Metadata))
	for key := range article.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "" {
			frontmatter[key] = article.Metadata[key]
		}
	}

	buf, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", err
	}
	return "---\n" + string(buf) + "---", nil
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

func (fm *FileManager) cleanupEmptyDirs(dir string) {
	libraryRoot := filepath.Join(fm.annaHome, "library")
	for dir != libraryRoot && dir != fm.annaHome && strings.HasPrefix(dir, libraryRoot) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ArticleExists reports whether an article file exists.
func (fm *FileManager) ArticleExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// EnsureLibrary creates the library directory structure if it doesn't exist.
func (fm *FileManager) EnsureLibrary(userID int64) error {
	libraryPath := filepath.Join(fm.annaHome, "library", fmt.Sprintf("%d", userID), "articles")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		return fmt.Errorf("create library: %w", err)
	}
	return nil
}

// ListArticles returns all article markdown files in a user's library.
func (fm *FileManager) ListArticles(userID int64) ([]string, error) {
	libraryPath := filepath.Join(fm.annaHome, "library", fmt.Sprintf("%d", userID), "articles")
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
func (fm *FileManager) RebuildIndex(userID int64) ([]string, error) {
	return fm.ListArticles(userID)
}
