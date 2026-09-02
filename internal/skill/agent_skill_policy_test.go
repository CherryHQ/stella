package skill

import (
	"context"
	"strings"
	"testing"
)

func TestSkillsToolAgentPolicyBlocksAllModelReachableReads(t *testing.T) {
	tool := newProjectionTool(t, &projectionReader{}, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{}).
		WithAgentSkillPolicy([]string{"builtin:stella"})
	ctx := context.Background()

	// Name, stable API ID, logical ref, and a file/reference read all resolve the
	// same winner before filtering. None can manufacture a lower-precedence or
	// direct-file bypass, and no helper directory is returned.
	for _, args := range []map[string]any{
		{"name": "stella"},
		{"name": "builtin-stella"},
		{"name": "builtin:stella"},
		{"name": "stella", "path": "references/anything.md"},
	} {
		out, err := skillAction(tool, "load").Execute(ctx, args)
		if err == nil || out != "" {
			t.Fatalf("skill_load(%#v) = %q, %v; disabled winner must be unavailable", args, out, err)
		}
	}

	for _, args := range []map[string]any{{"q": "stella"}} {
		out, err := skillAction(tool, "search").Execute(ctx, args)
		if err != nil {
			t.Fatalf("skill_installed_search(%#v): %v", args, err)
		}
		if strings.Contains(out, `"name": "stella"`) || strings.Contains(out, "<skill_dir>") {
			t.Fatalf("skill_installed_search(%#v) leaked disabled winner/helper: %s", args, out)
		}
	}
}
