package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore_Write_Read(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Read non-existent file returns empty.
	got, err := s.Read(FileFact)
	if err != nil {
		t.Fatalf("Read on empty: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	// Write and read back.
	want := "# Facts\n\n- User prefers Go."
	if err := s.Write(FileFact, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err = s.Read(FileFact)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// No tmp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("tmp file %q should not exist after atomic write", e.Name())
		}
	}
}

func TestStore_Write_Atomic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	original := "original content"
	if err := s.Write(FileFact, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Overwrite with new content.
	updated := "updated content"
	if err := s.Write(FileFact, updated); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := s.Read(FileFact)
	if got != updated {
		t.Fatalf("got %q, want %q", got, updated)
	}
}

func TestStore_Append_Search(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Search empty journal returns nil.
	results, err := s.Search("", "", 10)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil, got %v", results)
	}

	// Append entries.
	entries := []JournalEntry{
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), Tags: []string{"deploy"}, Text: "Deployed v1 to staging"},
		{Timestamp: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), Tags: []string{"deploy", "prod"}, Text: "Deployed v1 to production"},
		{Timestamp: time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC), Tags: []string{"meeting"}, Text: "Sprint planning for Q2"},
	}
	for _, e := range entries {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Search by query.
	results, err = s.Search("deployed", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Results should be reverse-chronological.
	if results[0].Text != "Deployed v1 to production" {
		t.Fatalf("expected production first, got %q", results[0].Text)
	}

	// Search by tag.
	results, err = s.Search("", "prod", 10)
	if err != nil {
		t.Fatalf("Search by tag: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Search with limit.
	results, err = s.Search("", "", 2)
	if err != nil {
		t.Fatalf("Search with limit: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Should be the 2 most recent.
	if results[0].Text != "Sprint planning for Q2" {
		t.Fatalf("expected sprint planning first, got %q", results[0].Text)
	}
}

func TestStore_Search_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	_ = s.Append(JournalEntry{Timestamp: time.Now(), Text: "Fixed Bug in AuthModule"})

	results, _ := s.Search("fixed bug", "", 10)
	if len(results) != 1 {
		t.Fatalf("expected case-insensitive match, got %d results", len(results))
	}
}

func TestStore_Search_TagCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	_ = s.Append(JournalEntry{Timestamp: time.Now(), Tags: []string{"Deploy"}, Text: "deployed"})

	results, _ := s.Search("", "deploy", 10)
	if len(results) != 1 {
		t.Fatalf("expected case-insensitive tag match, got %d results", len(results))
	}
}

func TestStore_JournalFileCreated(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	_ = s.Append(JournalEntry{Timestamp: time.Now(), Text: "test"})

	if _, err := os.Stat(filepath.Join(dir, "JOURNAL.jsonl")); err != nil {
		t.Fatalf("journal file should exist: %v", err)
	}
}

func TestStore_Dir(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if got := s.Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

func TestStore_Path(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	got := s.Path(FileFact)
	want := filepath.Join(dir, string(FileFact))
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestMemoryToolDefinition(t *testing.T) {
	s := NewStore(t.TempDir())
	tool := NewTool(s)
	def := tool.Definition()
	if def.Name != "memory" {
		t.Errorf("Name = %q, want %q", def.Name, "memory")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestMemoryToolUpdate(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	tool := NewTool(s)

	// Missing content
	_, err := tool.Execute(context.TODO(), map[string]any{"action": "update"})
	if err == nil {
		t.Fatal("expected error for empty content")
	}

	// Valid update
	result, err := tool.Execute(context.TODO(), map[string]any{
		"action":  "update",
		"content": "# Facts\n\n- Go is great.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Memory updated." {
		t.Errorf("result = %q", result)
	}

	// Verify written
	got, _ := s.Read(FileFact)
	if got != "# Facts\n\n- Go is great." {
		t.Errorf("stored content = %q", got)
	}
}

func TestMemoryToolAppend(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	tool := NewTool(s)

	// Missing text
	_, err := tool.Execute(context.TODO(), map[string]any{"action": "append"})
	if err == nil {
		t.Fatal("expected error for empty text")
	}

	// Valid append with tags as []any (JSON decoded form)
	result, err := tool.Execute(context.TODO(), map[string]any{
		"action": "append",
		"text":   "deployed v2",
		"tags":   []any{"deploy", "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify searchable
	entries, _ := s.Search("deployed", "", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Append with []string tags
	_, err = tool.Execute(context.TODO(), map[string]any{
		"action": "append",
		"text":   "another entry",
		"tags":   []string{"test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryToolSearch(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	tool := NewTool(s)

	// Search empty journal
	result, err := tool.Execute(context.TODO(), map[string]any{"action": "search"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "No matching journal entries found." {
		t.Errorf("result = %q", result)
	}

	// Add entries and search
	_ = s.Append(JournalEntry{Timestamp: time.Now(), Tags: []string{"deploy"}, Text: "deployed v1"})
	_ = s.Append(JournalEntry{Timestamp: time.Now(), Tags: []string{"meeting"}, Text: "sprint planning"})

	result, err = tool.Execute(context.TODO(), map[string]any{
		"action": "search",
		"query":  "deployed",
		"limit":  float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "deployed v1") {
		t.Errorf("result should contain entry: %s", result)
	}

	// Search by tag
	result, err = tool.Execute(context.TODO(), map[string]any{
		"action": "search",
		"tag":    "meeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sprint planning") {
		t.Errorf("result should contain meeting entry: %s", result)
	}
}

func TestMemoryToolUnknownAction(t *testing.T) {
	s := NewStore(t.TempDir())
	tool := NewTool(s)

	_, err := tool.Execute(context.TODO(), map[string]any{"action": "delete"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}
