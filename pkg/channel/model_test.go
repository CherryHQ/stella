package channel

import (
	"strings"
	"testing"
)

func TestParseModelArgs(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  claude/opus  ", "claude/opus"},
		{"", ""},
		{"gpt-4", "gpt-4"},
	}
	for _, tc := range tests {
		got := ParseModelArgs(tc.input)
		if got != tc.want {
			t.Errorf("ParseModelArgs(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestIndexModels(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-4"},
	}
	indexed := IndexModels(models)
	if len(indexed) != 2 {
		t.Fatalf("expected 2 indexed models, got %d", len(indexed))
	}
	if indexed[0].GlobalIdx != 1 {
		t.Errorf("expected GlobalIdx=1, got %d", indexed[0].GlobalIdx)
	}
	if indexed[1].GlobalIdx != 2 {
		t.Errorf("expected GlobalIdx=2, got %d", indexed[1].GlobalIdx)
	}
}

func TestFilterModels_MatchProvider(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-4"},
	}
	filtered := FilterModels(models, "anthropic")
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match, got %d", len(filtered))
	}
	if filtered[0].Provider != "anthropic" {
		t.Errorf("unexpected provider: %q", filtered[0].Provider)
	}
}

func TestFilterModels_MatchModel(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-4"},
	}
	filtered := FilterModels(models, "gpt")
	if len(filtered) != 1 || filtered[0].Model != "gpt-4" {
		t.Errorf("expected gpt-4, got %v", filtered)
	}
}

func TestFilterModels_CaseInsensitive(t *testing.T) {
	models := []ModelOption{
		{Provider: "Anthropic", Model: "Claude-3"},
	}
	filtered := FilterModels(models, "anthropic")
	if len(filtered) != 1 {
		t.Errorf("expected case-insensitive match, got %d results", len(filtered))
	}
}

func TestFilterModels_PreservesGlobalIdx(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-4"},
	}
	filtered := FilterModels(models, "claude-4")
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match, got %d", len(filtered))
	}
	// claude-4 is at index 2 (0-based), so GlobalIdx should be 3.
	if filtered[0].GlobalIdx != 3 {
		t.Errorf("expected GlobalIdx=3, got %d", filtered[0].GlobalIdx)
	}
}

func TestFindModelByName_Found(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
	}
	m, ok := FindModelByName(models, "anthropic/claude-3")
	if !ok {
		t.Fatal("expected to find model")
	}
	if m.Provider != "anthropic" || m.Model != "claude-3" {
		t.Errorf("unexpected model: %+v", m)
	}
}

func TestFindModelByName_CaseInsensitive(t *testing.T) {
	models := []ModelOption{
		{Provider: "Anthropic", Model: "Claude-3"},
	}
	_, ok := FindModelByName(models, "anthropic/claude-3")
	if !ok {
		t.Error("expected case-insensitive match")
	}
}

func TestFindModelByName_NotFound(t *testing.T) {
	models := []ModelOption{
		{Provider: "anthropic", Model: "claude-3"},
	}
	_, ok := FindModelByName(models, "openai/gpt-4")
	if ok {
		t.Error("expected not found")
	}
}

func TestFormatModelList_NoFilter(t *testing.T) {
	models := []IndexedModel{
		{ModelOption: ModelOption{Provider: "anthropic", Model: "claude-3"}, GlobalIdx: 1},
	}
	out := FormatModelList(models, "")
	if !strings.Contains(out, "anthropic/claude-3") {
		t.Errorf("expected model in output, got %q", out)
	}
	if strings.Contains(out, "filter:") {
		t.Error("expected no filter annotation for empty query")
	}
}

func TestFormatModelList_WithFilter(t *testing.T) {
	models := []IndexedModel{
		{ModelOption: ModelOption{Provider: "anthropic", Model: "claude-3"}, GlobalIdx: 1},
	}
	out := FormatModelList(models, "claude")
	if !strings.Contains(out, `"claude"`) {
		t.Errorf("expected filter annotation, got %q", out)
	}
}
