package plugin

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestResourcePayloadFromAgentPackagePreservesSkillMetadata(t *testing.T) {
	payload, err := ResourcePayloadFromAgentPackage(&agentpackage.Package{
		Manifest: agentpackage.Manifest{Name: "demo", Version: "1.0.0"},
		Skills: []agentpackage.Skill{{
			Name:        "docs",
			Path:        "skills/docs/SKILL.md",
			Description: "package documentation",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Skills) != 1 {
		t.Fatalf("skills = %#v, want one declaration", payload.Skills)
	}
	got := payload.Skills[0]
	if got.Name != "docs" || got.Path != "skills/docs/SKILL.md" || got.Description != "package documentation" {
		t.Fatalf("skill payload = %#v", got)
	}
}
