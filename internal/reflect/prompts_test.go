package reflect

import (
	"strings"
	"testing"
)

func TestCombinedReviewPrompt_ExplicitlyProhibitsConstraintActions(t *testing.T) {
	for _, action := range []string{"constraint_add", "constraint_remove", "constraint_list"} {
		if !strings.Contains(combinedReviewPrompt, action) {
			t.Errorf("expected reflect prompt to mention %q as off-limits", action)
		}
	}

	// The prohibition should appear in the Constraints section.
	if !strings.Contains(combinedReviewPrompt, "## Constraints") {
		t.Error("expected ## Constraints section in reflect prompt")
	}
	if !strings.Contains(combinedReviewPrompt, "off-limits") {
		t.Error("expected 'off-limits' in reflect prompt constraints section")
	}
}

func TestCombinedReviewPrompt_UsesFactSubjectRouting(t *testing.T) {
	for _, phrase := range []string{
		"subject=user",
		"subject=agent",
		"subject=world",
		"Do NOT use the skills tool for knowledge facts",
	} {
		if !strings.Contains(combinedReviewPrompt, phrase) {
			t.Errorf("expected reflect prompt to contain %q", phrase)
		}
	}
	if strings.Contains(combinedReviewPrompt, `knowledge_type="fact"`) {
		t.Error("reflect prompt should not route knowledge facts through skill metadata")
	}
}

func TestCombinedReviewPrompt_DefersWorldFactWrites(t *testing.T) {
	for _, phrase := range []string{
		"Identify subject=world candidates",
		"Do NOT create or write subject=world facts",
	} {
		if !strings.Contains(combinedReviewPrompt, phrase) {
			t.Errorf("expected reflect prompt to contain %q", phrase)
		}
	}
	if strings.Contains(combinedReviewPrompt, "Create a subject=world fact") {
		t.Error("reflect prompt should not claim this review can create subject=world facts")
	}
}
