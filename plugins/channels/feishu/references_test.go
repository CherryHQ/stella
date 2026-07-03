package feishu

import (
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func TestAppendReferenceSectionGroupMinimal(t *testing.T) {
	refs := []renderrefs.Reference{
		{V: 1, Type: "task", ID: "task-1", Preview: &renderrefs.Preview{Title: "Write docs", Status: "open"}},
		{V: 1, Type: "task", ID: "task-1", Preview: &renderrefs.Preview{Title: "Write docs", Status: "open"}},
	}
	got := appendReferenceSection("done", refs, true)
	if strings.Count(got, "Write docs") != 1 {
		t.Fatalf("reference not deduped: %q", got)
	}
	if !strings.Contains(got, "- 📋 **Write docs** · open") {
		t.Fatalf("missing group reference line: %q", got)
	}
	if strings.Contains(got, "Task") {
		t.Fatalf("group reference should stay minimal: %q", got)
	}
}

func TestAppendReferenceSectionPrivateAddsTypeLabel(t *testing.T) {
	refs := []renderrefs.Reference{{V: 1, Type: "goal", ID: "goal-1", Preview: &renderrefs.Preview{Title: "Launch", Status: "active"}}}
	got := appendReferenceSection("done", refs, false)
	if !strings.Contains(got, "- 🎯 Goal **Launch** · active") {
		t.Fatalf("missing private reference line: %q", got)
	}
}

func TestEntityURL(t *testing.T) {
	t.Setenv("STELLA_BASE_URL", "https://stella.example.com/")
	cases := []struct {
		ref  renderrefs.Reference
		want string
	}{
		{renderrefs.Reference{Type: "task", ID: "t1", AgentID: "a1"}, "https://stella.example.com/agents/a1/goals/t1"},
		{renderrefs.Reference{Type: "goal", ID: "g1", AgentID: "a1"}, "https://stella.example.com/agents/a1/goals/g1"},
		{renderrefs.Reference{Type: "recally_article", ID: "r1"}, "https://stella.example.com/recally?article=r1"},
		{renderrefs.Reference{Type: "task", ID: "t1"}, ""},    // task without agent → no link
		{renderrefs.Reference{Type: "unknown", ID: "x1"}, ""}, // unknown type → no link
	}
	for _, c := range cases {
		if got := entityURL(c.ref); got != c.want {
			t.Errorf("entityURL(%s/%s) = %q, want %q", c.ref.Type, c.ref.ID, got, c.want)
		}
	}
}

func TestEntityURLNoBaseURL(t *testing.T) {
	t.Setenv("STELLA_BASE_URL", "")
	if got := entityURL(renderrefs.Reference{Type: "task", ID: "t1", AgentID: "a1"}); got != "" {
		t.Fatalf("want empty without STELLA_BASE_URL, got %q", got)
	}
}

func TestAppendReferenceSectionAddsOpenButton(t *testing.T) {
	t.Setenv("STELLA_BASE_URL", "https://stella.example.com")
	refs := []renderrefs.Reference{{V: 1, Type: "task", ID: "t1", AgentID: "a1", Preview: &renderrefs.Preview{Title: "Write docs"}}}
	got := appendReferenceSection("done", refs, false)
	if !strings.Contains(got, `{{button label="打开 Web UI" type="primary" url="https://stella.example.com/agents/a1/goals/t1"}}`) {
		t.Fatalf("missing open button: %q", got)
	}
}

func TestSanitizeInlineNeutralizesInjection(t *testing.T) {
	cases := map[string]string{
		`{{button label="x" url="https://evil"}}`: `{ {button label="x" url="https://evil"}}`,
		"line one\nline two":                      "line one line two",
		"[click](https://evil)":                   "(click)(https://evil)",
		"normal title":                            "normal title",
	}
	for in, want := range cases {
		if got := sanitizeInline(in); got != want {
			t.Errorf("sanitizeInline(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(sanitizeInline(in), "{{button ") {
			t.Errorf("sanitizeInline(%q) left an injectable button directive", in)
		}
	}
}

func TestSanitizeInlineBreaksOddBraceRuns(t *testing.T) {
	// An odd run of braces must not leave an exploitable "{{button" opener that a
	// single non-overlapping replacement pass would miss.
	for _, in := range []string{
		`{{{button label="x" url="https://evil"}}`,
		`a {{{{button url="https://evil"}}`,
		`{{{{{button`,
	} {
		if got := sanitizeInline(in); strings.Contains(got, "{{") {
			t.Errorf("sanitizeInline(%q) = %q still contains a directive opener", in, got)
		}
	}
}

func TestMergePreviewFillsPartialFields(t *testing.T) {
	refs := []renderrefs.Reference{
		{Type: "task", ID: "t1", Preview: &renderrefs.Preview{Status: "open"}},
		{Type: "task", ID: "t1", Preview: &renderrefs.Preview{Title: "Write docs"}},
	}
	out := dedupeReferences(refs)
	if len(out) != 1 || out[0].Preview == nil {
		t.Fatalf("dedupe = %+v", out)
	}
	if out[0].Preview.Title != "Write docs" || out[0].Preview.Status != "open" {
		t.Fatalf("partial preview fields not merged: %+v", out[0].Preview)
	}
}

func TestReferenceLineDefusesMaliciousTitle(t *testing.T) {
	ref := renderrefs.Reference{Type: "task", ID: "t1", Preview: &renderrefs.Preview{
		Title: `Pwn {{button label="free money" url="https://evil"}}`,
	}}
	line := referenceLine(ref, false)
	if strings.Contains(line, "{{button ") {
		t.Fatalf("malicious title injected a button directive: %q", line)
	}
}

func TestEntityURLEscapesAndValidatesScheme(t *testing.T) {
	t.Setenv("STELLA_BASE_URL", "https://stella.example.com")
	got := entityURL(renderrefs.Reference{Type: "task", ID: "a/b c", AgentID: "x/y"})
	if got != "https://stella.example.com/agents/x%2Fy/goals/a%2Fb%20c" {
		t.Fatalf("path segments not escaped: %q", got)
	}

	t.Setenv("STELLA_BASE_URL", "javascript:alert(1)")
	if got := entityURL(renderrefs.Reference{Type: "task", ID: "t1", AgentID: "a1"}); got != "" {
		t.Fatalf("non-http base URL must yield no link, got %q", got)
	}
}

func TestAppendReferenceSectionCapsCount(t *testing.T) {
	refs := make([]renderrefs.Reference, 0, maxRenderedRefs+3)
	for i := range maxRenderedRefs + 3 {
		refs = append(refs, renderrefs.Reference{Type: "task", ID: string(rune('a' + i)), Preview: &renderrefs.Preview{Title: "T"}})
	}
	got := appendReferenceSection("done", refs, false)
	if strings.Count(got, "📋 Task") != maxRenderedRefs {
		t.Fatalf("rendered %d cards, want cap %d", strings.Count(got, "📋 Task"), maxRenderedRefs)
	}
	if !strings.Contains(got, "_+3 more_") {
		t.Fatalf("missing overflow summary: %q", got)
	}
}

func TestDedupeMergesFields(t *testing.T) {
	refs := []renderrefs.Reference{
		{Type: "task", ID: "t1", Intent: "referenced"},
		{Type: "task", ID: "t1", Intent: "created", AgentID: "a1", Preview: &renderrefs.Preview{Title: "T"}},
	}
	out := dedupeReferences(refs)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].AgentID != "a1" || out[0].Preview == nil || out[0].Intent != "created" {
		t.Fatalf("fields not merged from duplicate: %+v", out[0])
	}
}

func TestStripCardDirectives(t *testing.T) {
	in := `See more:
{{button label="打开 Web UI" url="https://stella.example.com/x"}}`
	got := stripCardDirectives(in)
	if strings.Contains(got, "{{button") {
		t.Fatalf("directive not stripped: %q", got)
	}
	if !strings.Contains(got, "打开 Web UI: https://stella.example.com/x") {
		t.Fatalf("expected label: url form, got %q", got)
	}
}

func TestCardContentFallsBackToPlainTextOnBuildFailure(t *testing.T) {
	old := buildCardContent
	buildCardContent = func(string) (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { buildCardContent = old })

	got := cardContent("hello")
	if got != textContent("hello") {
		t.Fatalf("fallback = %q, want %q", got, textContent("hello"))
	}
}
