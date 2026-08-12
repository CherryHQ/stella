package resources

import (
	"io/fs"
	"strings"
	"testing"
)

func TestBuiltinSkillsUseSequentialSessionSurface(t *testing.T) {
	for _, path := range []string{
		"skills/system/recally/references/rss-workflow.md",
		"skills/system/skill-creator/SKILL.md",
	} {
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if !strings.Contains(text, "session.create") || !strings.Contains(strings.ToLower(text), "sequential") {
			t.Fatalf("%s must teach the synchronous sequential Session workflow", path)
		}
	}

	err := fs.WalkDir(fsys, "skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(content))
		for _, removed := range []string{
			"`delegate` tool", "spawn one delegate", "spawn two delegates",
			"grader delegate", "parallel via delegates", "delegate task completes", "baseline delegate",
		} {
			if strings.Contains(lower, removed) {
				t.Fatalf("%s still teaches removed delegate-tool behavior %q", path, removed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
