package resources

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestTapWebSkillUsesLongInteractiveFlag(t *testing.T) {
	content, err := fs.ReadFile(fsys, "skills/system/tap-web/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if regexp.MustCompile(`snapshot(\s+|\("?)-i\b`).MatchString(text) {
		t.Fatal("tap-web SKILL.md must not use the short snapshot interactive flag")
	}
	if !strings.Contains(text, "snapshot --interactive") {
		t.Fatal("tap-web SKILL.md must document snapshot --interactive")
	}
}

func TestTapWebSkillUsesLightpandaOnly(t *testing.T) {
	content, err := fs.ReadFile(fsys, "skills/system/tap-web/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "AGENT_BROWSER_ENGINE=lightpanda") {
		t.Fatal("tap-web SKILL.md must document the sandbox Lightpanda engine")
	}
	for _, forbidden := range []string{"AGENT_BROWSER_ENGINE=chrome", "Fall back to Chrome"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("tap-web SKILL.md must not recommend Chrome fallback: found %q", forbidden)
		}
	}
}
