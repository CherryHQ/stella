package feishu

import (
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/renderrefs"
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
		{renderrefs.Reference{Type: "task", ID: "t1", AgentID: "a1"}, "https://stella.example.com/agents/a1/tasks/t1"},
		{renderrefs.Reference{Type: "goal", ID: "g1", AgentID: "a1"}, "https://stella.example.com/agents/a1/tasks/goals/g1"},
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
	if !strings.Contains(got, `{{button label="打开 Web UI" type="primary" url="https://stella.example.com/agents/a1/tasks/t1"}}`) {
		t.Fatalf("missing open button: %q", got)
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
