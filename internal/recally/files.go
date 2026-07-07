// Package recally provides file operations for article storage.
package recally

import (
	"fmt"
	"os"
	"strings"
)

// FileManager reads legacy article files from disk. Bodies now live in
// PostgreSQL; the disk mirror is no longer written and the read path no longer
// consults it, so the only remaining reader is the startup backfill
// (BackfillMissingContent), which drains legacy file-only bodies into the DB.
type FileManager struct {
	stellaHome string
}

// newFileManager creates a new FileManager instance.
func newFileManager(stellaHome string) *FileManager {
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
