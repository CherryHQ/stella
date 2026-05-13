package prompt_test

import (
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
