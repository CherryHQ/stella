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
