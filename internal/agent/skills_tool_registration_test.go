package agent

import (
	"os"
	"strings"
	"testing"
)

// TestSkillsToolRegistrationIsReadOnlyAndAuthorized freezes the agent-facing
// skills tool registration: it must stay restricted to read actions and must
// inject the ResourceSkill read PEP. A regression that broadens the action set or
// drops the authorizer would let an agent turn read or mutate DB skills outside
// the policy, so this guards runner_impl against exactly that.
func TestSkillsToolRegistrationIsReadOnlyAndAuthorized(t *testing.T) {
	src := readSource(t, "runner_impl.go")
	if !strings.Contains(src, `WithReadAuthorizer(cfg.SkillReadAuthorizer)`) {
		t.Fatal("runner_impl skills tool must be constructed WithReadAuthorizer(cfg.SkillReadAuthorizer)")
	}
	if !strings.Contains(src, `WithActionsOnly("search_installed", "load")`) {
		t.Fatal(`runner_impl skills tool must be restricted to WithActionsOnly("search_installed", "load")`)
	}
	for _, write := range []string{`"install"`, `"remove"`, `"create"`, `"patch"`, `"deprecate"`} {
		if strings.Contains(src, "WithActionsOnly(") && strings.Contains(actionsOnlyArgs(src), write) {
			t.Fatalf("runner_impl skills tool must not expose write action %s", write)
		}
	}
}

// actionsOnlyArgs returns the argument text of the WithActionsOnly(...) call so a
// write action anywhere else in the file (e.g. a comment) does not trip the guard.
func actionsOnlyArgs(src string) string {
	_, after, ok := strings.Cut(src, "WithActionsOnly(")
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, ")"); ok {
		return before
	}
	return rest
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
