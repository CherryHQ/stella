package server

import (
	"strings"
	"testing"
)

func TestRenderMarkdownPage(t *testing.T) {
	md := []byte("# Hello\n\nThis is **bold** and a [link](https://example.com).\n\n```go\nfmt.Println(\"hi\")\n```\n")
	out, err := renderMarkdownPage("Test Article", "Alice", "2026-06-01", md)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if !strings.Contains(html, "<title>Test Article</title>") {
		t.Error("missing title")
	}
	if !strings.Contains(html, "Alice") {
		t.Error("missing author")
	}
	if !strings.Contains(html, "2026-06-01") {
		t.Error("missing expiry")
	}
	if !strings.Contains(html, "<strong>bold</strong>") {
		t.Error("missing bold rendering")
	}
	if !strings.Contains(html, "<a href=\"https://example.com\">link</a>") {
		t.Error("missing link rendering")
	}
	if !strings.Contains(html, "<code") {
		t.Error("missing code block rendering")
	}
}

func TestRenderMarkdownPageNoMeta(t *testing.T) {
	md := []byte("Just a paragraph.")
	out, err := renderMarkdownPage("Untitled", "", "", md)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if strings.Contains(html, `class="meta"`) {
		t.Error("meta section should be hidden when no author or expiry")
	}
}

func TestRenderMarkdownPageGFMTable(t *testing.T) {
	md := []byte("| A | B |\n|---|---|\n| 1 | 2 |\n")
	out, err := renderMarkdownPage("Table Test", "", "", md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<table>") {
		t.Error("GFM table not rendered")
	}
}
