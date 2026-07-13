package server_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	apitypes "github.com/CherryHQ/stella/api/types"
)

func TestLifecycleSchemasJSONContract(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	nextPageToken := "next-page"
	removalSource := apitypes.RemovalSource("curator")
	skillRemovalSource := apitypes.RemovalSource("manual")
	installVersion := "1.2.3"
	convertToManual := true

	// Validate the generated enum contract independently from any HTTP handler.
	for _, value := range []apitypes.KnowledgeItemSource{"manual", "reflect"} {
		if !value.Valid() {
			t.Errorf("KnowledgeItemSource(%q).Valid() = false", value)
		}
	}
	if apitypes.KnowledgeItemSource("unknown").Valid() {
		t.Error("unknown KnowledgeItemSource should be invalid")
	}
	for _, value := range []apitypes.RemovalSource{"manual", "curator"} {
		if !value.Valid() {
			t.Errorf("RemovalSource(%q).Valid() = false", value)
		}
	}
	for _, value := range []apitypes.SkillCreatedBy{"manual", "reflect"} {
		if !value.Valid() {
			t.Errorf("SkillCreatedBy(%q).Valid() = false", value)
		}
	}
	knowledge := apitypes.KnowledgeItem{
		Id:              "knowledge-1",
		Content:         "remember this",
		Source:          apitypes.KnowledgeItemSource("reflect"),
		CreatedAt:       now,
		UpdatedAt:       now,
		IsRestorable:    true,
		RemovalSource:   &removalSource,
		DeprecatedAt:    &now,
		RestoreDeadline: &now,
	}
	assertJSONField(t, knowledge, "source", "reflect")
	assertJSONField(t, knowledge, "is_restorable", true)
	assertJSONField(t, knowledge, "removal_source", "curator")
	assertJSONField(t, knowledge, "created_at", now.Format(time.RFC3339))

	knowledgeList := apitypes.KnowledgeList{
		Knowledge:     []apitypes.KnowledgeItem{knowledge},
		TotalSize:     1,
		NextPageToken: &nextPageToken,
	}
	assertJSONField(t, knowledgeList, "total_size", 1.0)
	assertJSONField(t, knowledgeList, "next_page_token", "next-page")

	assertJSONField(t, apitypes.CreateKnowledgeRequest{Content: "new knowledge"}, "content", "new knowledge")
	assertJSONField(t, apitypes.UpdateKnowledgeRequest{Content: "updated knowledge"}, "content", "updated knowledge")
	assertJSONField(t, apitypes.ChangelogList{Entries: []apitypes.ChangelogEntry{}, NextPageToken: &nextPageToken}, "next_page_token", "next-page")

	skill := apitypes.Skill{
		Version:          &installVersion,
		LifecycleVersion: 4,
		CreatedBy:        apitypes.SkillCreatedBy("manual"),
		IsRestorable:     true,
		RemovalSource:    &skillRemovalSource,
		DeprecatedAt:     &now,
		RestoreDeadline:  &now,
	}
	assertJSONField(t, skill, "version", "1.2.3")
	assertJSONField(t, skill, "lifecycle_version", 4.0)
	assertJSONField(t, skill, "created_by", "manual")
	assertJSONField(t, skill, "is_restorable", true)
	assertJSONField(t, skill, "removal_source", "manual")

	scopeCounts := apitypes.SkillScopeCounts{All: 4, System: 1, Agent: 1, User: 1, Project: 1}
	skillList := apitypes.SkillList{
		Skills:        []apitypes.Skill{skill},
		TotalSize:     1,
		ScopeCounts:   scopeCounts,
		NextPageToken: &nextPageToken,
	}
	assertJSONField(t, skillList, "total_size", 1.0)
	assertJSONField(t, skillList, "scope_counts", map[string]any{"all": 4.0, "system": 1.0, "agent": 1.0, "user": 1.0, "project": 1.0})
	assertJSONField(t, skillList, "next_page_token", "next-page")
	assertJSONField(t, apitypes.UpdateSkillRequest{ConvertToManual: &convertToManual}, "convert_to_manual", true)

	assertRemovalSourceNull(t)
}

func assertRemovalSourceNull(t *testing.T) {
	t.Helper()

	var knowledge apitypes.KnowledgeItem
	if err := json.Unmarshal([]byte(`{"removal_source":null}`), &knowledge); err != nil {
		t.Fatalf("unmarshal KnowledgeItem null removal_source: %v", err)
	}
	if knowledge.RemovalSource != nil {
		t.Fatalf("KnowledgeItem removal_source = %v, want nil", *knowledge.RemovalSource)
	}

	var skill apitypes.Skill
	if err := json.Unmarshal([]byte(`{"removal_source":null}`), &skill); err != nil {
		t.Fatalf("unmarshal Skill null removal_source: %v", err)
	}
	if skill.RemovalSource != nil {
		t.Fatalf("Skill removal_source = %v, want nil", *skill.RemovalSource)
	}
}

func assertJSONField(t *testing.T, value any, field string, want any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal %T: %v", value, err)
	}
	if got := object[field]; !reflect.DeepEqual(got, want) {
		t.Errorf("%T JSON field %q = %#v, want %#v", value, field, got, want)
	}
}
