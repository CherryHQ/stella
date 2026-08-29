package tools

import (
	"strings"
	"testing"
)

// The shapes below mirror what toolgen emits for recally_save_article, the one
// batch tool in the tree: a wrapper with a single array property whose items
// carry the article fields.
type saveArticleItem struct {
	ContentPath string `json:"content_path,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
}

type saveArticleInput struct {
	Items []saveArticleItem `json:"articles,omitempty"`
}

func TestDecodeInputStrictAcceptsDeclaredFields(t *testing.T) {
	var in saveArticleInput
	args := map[string]any{"articles": []any{map[string]any{
		"url":          "https://example.com/a",
		"title":        "A",
		"content_path": "$TMPDIR/a.md",
	}}}
	if err := DecodeInputStrict(args, &in, []string{"articles"}); err != nil {
		t.Fatalf("DecodeInputStrict: %v", err)
	}
	if len(in.Items) != 1 || in.Items[0].ContentPath != "$TMPDIR/a.md" {
		t.Fatalf("decoded=%#v", in.Items)
	}
}

func TestDecodeInputStrictRejectsUnknownFieldAndListsWhatIsAccepted(t *testing.T) {
	type getArticleInput struct {
		ID string `json:"id,omitempty"`
	}
	var in getArticleInput
	err := DecodeInputStrict(map[string]any{"id": "a1", "titel": "typo"}, &in, []string{"id"})
	if err == nil {
		t.Fatal("unknown field accepted")
	}
	// The model has to be able to fix the call from the error alone, so it
	// names the offending field and every field the tool does take.
	if !strings.Contains(err.Error(), `unknown field "titel"`) || !strings.Contains(err.Error(), "this tool accepts: id") {
		t.Fatalf("error=%q, want the unknown field and the accepted list", err)
	}
}

func TestDecodeInputStrictRejectsUnknownBatchItemField(t *testing.T) {
	var in saveArticleInput
	args := map[string]any{"articles": []any{map[string]any{"url": "https://example.com/a", "titel": "typo"}}}
	err := DecodeInputStrict(args, &in, []string{"articles"})
	if err == nil {
		t.Fatal("unknown batch item field accepted")
	}
	if !strings.Contains(err.Error(), `unknown field "titel"`) {
		t.Fatalf("error=%q, want the unknown field named", err)
	}
	// The accepted list walks into the item so the model can see where the
	// field it meant actually lives.
	if !strings.Contains(err.Error(), "articles[].title") {
		t.Fatalf("error=%q, want batch item fields listed", err)
	}
}

func TestDecodeInputStrictStillReportsMissingRequiredField(t *testing.T) {
	var in saveArticleInput
	if err := DecodeInputStrict(map[string]any{}, &in, []string{"articles"}); err == nil ||
		!strings.Contains(err.Error(), `missing required field "articles"`) {
		t.Fatalf("err=%v, want a missing required field error", err)
	}
}

// DecodeInput stays lenient: a union tool's hoisted schema legitimately carries
// every action's fields, so an unknown one there is not an error.
func TestDecodeInputStaysLenient(t *testing.T) {
	type unionInput struct {
		Action string `json:"action,omitempty"`
	}
	var in unionInput
	if err := DecodeInput(map[string]any{"action": "list", "title": "x"}, &in, []string{"action"}); err != nil {
		t.Fatalf("DecodeInput: %v", err)
	}
}
