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

func TestCardContentFallsBackToPlainTextOnBuildFailure(t *testing.T) {
	old := buildCardContent
	buildCardContent = func(string) (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { buildCardContent = old })

	got := cardContent("hello")
	if got != textContent("hello") {
		t.Fatalf("fallback = %q, want %q", got, textContent("hello"))
	}
}
