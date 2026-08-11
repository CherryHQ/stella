package prompt_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
)

func TestDefaultSystemPrompt(t *testing.T) {
	got := prompt.DefaultSystemPrompt()
	if got == "" {
		t.Error("expected non-empty default system prompt")
	}
}

func TestDefaultAgentSoul(t *testing.T) {
	got := prompt.DefaultAgentSoul()
	if got == "" {
		t.Error("expected non-empty default agent soul")
	}
}

func TestFilesystemPromptOperationalContract(t *testing.T) {
	got := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella."})
	for _, want := range []string{
		"If `$STELLA_ASSETS_DIR` is available, put user uploads and final durable deliverables there; otherwise keep them under `$HOME`.",
		"XDG, mise, and Lark directories are tool-managed; do not choose them for files.",
		"`read`, `write`, and `edit` understand approved variables.",
		"Never hardcode `/workspace`, `/user`, or `/tmp`.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("filesystem guidance missing %q:\n%s", want, got)
		}
	}
}

func TestRecallGuidanceMatchesUnifiedAgentActions(t *testing.T) {
	systemPrompt := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella."})
	stellaSkill, err := os.ReadFile("../../../resources/skills/system/stella/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for surface, content := range map[string]string{
		"system prompt": systemPrompt,
		"Stella skill":  string(stellaSkill),
	} {
		for _, action := range []string{"memory.search", "memory.read", "library.search", "session.list", "session.get", "session.create", "session.send"} {
			if !strings.Contains(content, action) {
				t.Errorf("%s omitted model-facing action %q", surface, action)
			}
		}
		for _, stale := range []string{"session.find", "memory.search_knowledge", "memory.profile_get"} {
			if strings.Contains(content, stale) {
				t.Errorf("%s still teaches removed action %q", surface, stale)
			}
		}
	}
}
