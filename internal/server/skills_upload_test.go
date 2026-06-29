package server

import "testing"

func TestNormalizeUploadedSkillPathSkipsHiddenDirectories(t *testing.T) {
	paths := []string{
		"my-skill/.git/config",
		"my-skill/.git/objects/e2/f087be6e17ae5f0a8dbdf7fb208b77731ec41a",
		".git/config",
		"my-skill/.data/attachments/gmail_refund/591/image.png",
	}
	for _, input := range paths {
		name, skip, err := normalizeUploadedSkillPath(input)
		if err != nil {
			t.Fatalf("normalizeUploadedSkillPath(%q) error = %v", input, err)
		}
		if !skip {
			t.Fatalf("normalizeUploadedSkillPath(%q) skip = false, name = %q", input, name)
		}
	}
}

func TestNormalizeUploadedSkillPathDoesNotSkipAllowedDotOrGitNamedFiles(t *testing.T) {
	paths := []string{
		"my-skill/docs/git-notes.md",
		"my-skill/.env",
	}
	for _, input := range paths {
		name, skip, err := normalizeUploadedSkillPath(input)
		if err != nil {
			t.Fatalf("normalizeUploadedSkillPath(%q) error = %v", input, err)
		}
		if skip {
			t.Fatalf("normalizeUploadedSkillPath(%q) skipped allowed file", input)
		}
		if name != input {
			t.Fatalf("normalizeUploadedSkillPath(%q) name = %q", input, name)
		}
	}
}
