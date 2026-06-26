package skills

import "testing"

func TestSkillsSchemaDoesNotExposeKnowledgeType(t *testing.T) {
	props, ok := skillsInputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing or malformed: %#v", skillsInputSchema["properties"])
	}
	key := "knowledge" + "_" + "type"
	if _, ok := props[key]; ok {
		t.Fatal("skills tool must not expose legacy knowledge classification; use the knowledge tool for fact/context entries")
	}
}
