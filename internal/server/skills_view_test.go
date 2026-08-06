package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestSkillViewsPreserveContentDigest(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	stored := storedSkillToView(skills.Skill{ID: "stored", ContentDigest: digest}, nil)
	if stored.ContentDigest != digest {
		t.Fatalf("stored view digest = %q, want %q", stored.ContentDigest, digest)
	}

	resolved := resolvedSkillToView(skills.ResolvedSkill{Skill: pkgplugins.Skill{ID: "resolved", ContentDigest: digest}})
	if resolved.ContentDigest != digest {
		t.Fatalf("resolved view digest = %q, want %q", resolved.ContentDigest, digest)
	}
}
