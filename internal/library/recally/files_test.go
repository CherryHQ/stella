package recally

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mirror writer is gone; the startup backfill only has to strip YAML
// frontmatter from legacy on-disk files as it drains them into PostgreSQL.
func TestFileManager_ReadArticleStripsFrontmatter(t *testing.T) {
	tempDir := t.TempDir()
	fm := newFileManager(tempDir)

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
