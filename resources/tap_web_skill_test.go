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
