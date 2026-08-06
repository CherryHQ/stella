package main

import (
	"slices"
	"testing"
)

func TestStructuredGroupMemoryToolExcludesPrivateActions(t *testing.T) {
	want := []string{"status", "search", "describe", "expand", "get_message"}
	if !slices.Equal(structuredGroupMemoryActions, want) {
		t.Fatalf("structured group memory actions = %v, want %v", structuredGroupMemoryActions, want)
	}
	for _, private := range []string{
		"profile_get",
		"profile_update",
		"profile_history",
		"profile_rollback",
		"soul_get",
		"soul_update",
		"constraint_list",
		"constraint_add",
		"constraint_remove",
		"search_knowledge",
	} {
		if slices.Contains(structuredGroupMemoryActions, private) {
			t.Fatalf("structured group memory exposed private action %q", private)
		}
	}
}
