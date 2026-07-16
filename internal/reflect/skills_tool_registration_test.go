package reflect

import (
	"os"
	"strings"
	"testing"
)

// TestReviewerSkillsToolRegistrationIsAuthorized freezes the reflect reviewer's
// skills tool. The production prompt drives create/patch, so those stay
// enabled alongside search_installed/load — but every read and write must be
// gated by the ResourceSkill PEP (read + write authorizers injected), and the
// broader remove/install/search/list actions must NOT be exposed. A regression
// that drops an authorizer, opens a broader action, or reverts to the
// unrestricted NewTool form would let the reviewer bypass ResourceSkill.
func TestReviewerSkillsToolRegistrationIsAuthorized(t *testing.T) {
	b, err := os.ReadFile("conversation_review.go")
	if err != nil {
		t.Fatalf("read conversation_review.go: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, `WithReadAuthorizer(s.skillReadAuthz)`) {
		t.Fatal("reviewer skills tool must inject WithReadAuthorizer(s.skillReadAuthz)")
	}
	if !strings.Contains(src, `WithWriteAuthorizer(s.skillToolWriteAuthz)`) {
		t.Fatal("reviewer skills tool must inject WithWriteAuthorizer(s.skillToolWriteAuthz)")
	}
	if !strings.Contains(src, `WithActionsOnly("search_installed", "load", "create", "patch")`) {
		t.Fatal(`reviewer skills tool must be restricted to WithActionsOnly("search_installed", "load", "create", "patch")`)
	}
	// The unrestricted NewTool(...) form (no action allowlist) must not reappear.
	if strings.Contains(src, `skillstool.NewTool(s.skillStore, "", ""),`) {
		t.Fatal("reviewer skills tool must not be registered unrestricted")
	}
	// The broader write/discovery actions stay unavailable in the reviewer tool.
	args := reviewerActionsOnlyArgs(src)
	for _, forbidden := range []string{`"remove"`, `"install"`, `"search"`, `"list"`, `"deprecate"`} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("reviewer skills tool must not expose action %s", forbidden)
		}
	}
}

// reviewerActionsOnlyArgs returns the argument text of the WithActionsOnly(...)
// call so a forbidden action elsewhere in the file does not trip the guard.
func reviewerActionsOnlyArgs(src string) string {
	_, after, ok := strings.Cut(src, "WithActionsOnly(")
	if !ok {
		return ""
	}
	if before, _, ok := strings.Cut(after, ")"); ok {
		return before
	}
	return after
}
