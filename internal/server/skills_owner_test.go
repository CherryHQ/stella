package server

import (
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/skill"
)

func TestSkillViewUsesRegistryOwnerAndIgnoresMutableMetadata(t *testing.T) {
	merged := skill.NewService().ListMerged(nil, nil)
	var pluginSkill skill.ResolvedSkill
	for _, candidate := range merged {
		if candidate.Name == "lark-cli" {
			pluginSkill = candidate
			break
		}
	}
	if pluginSkill.Name == "" {
		t.Fatal("lark-cli builtin skill missing")
	}
	if got := resolvedSkillToView(pluginSkill).OwnerPluginID; got != "tool/lark-cli" {
		t.Fatalf("owner_plugin_id = %q, want tool/lark-cli", got)
	}

	forged := skill.Skill{Name: "forged", Scope: "system", Metadata: json.RawMessage(`{"owner_plugin_id":"tool/lark-cli","owner_plugin":"tool/lark-cli"}`)}
	if got := storedSkillToView(forged, nil).OwnerPluginID; got != "" {
		t.Fatalf("forged managed owner_plugin_id = %q, want empty", got)
	}
}
