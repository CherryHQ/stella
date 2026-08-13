package skills

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
		{"action": "load", "name": "stella"},
		{"action": "load", "name": "builtin-stella"},
		{"action": "load", "name": "builtin:stella"},
		{"action": "load", "name": "stella", "path": "references/anything.md"},
	} {
		out, err := tool.Execute(ctx, args)
		if err == nil || out != "" {
			t.Fatalf("Execute(%#v) = %q, %v; disabled winner must be unavailable", args, out, err)
		}
	}

	for _, args := range []map[string]any{{"action": "search_installed", "query": "stella"}} {
		out, err := tool.Execute(ctx, args)
		if err != nil {
			t.Fatalf("Execute(%#v): %v", args, err)
		}
		if strings.Contains(out, `"name": "stella"`) || strings.Contains(out, "<skill_dir>") {
			t.Fatalf("Execute(%#v) leaked disabled winner/helper: %s", args, out)
		}
	}
}
