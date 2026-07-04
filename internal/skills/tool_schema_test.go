package skills

import "testing"

func TestSkillsSchemaDoesNotExposeKnowledgeType(t *testing.T) {
	props, ok := skillsInputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing or malformed: %#v", skillsInputSchema["properties"])
	}
	if _, ok := props["knowledge_type"]; ok {
		t.Fatal("skills tool must not expose legacy knowledge classification; use facts-backed knowledge instead")
	}
}
