package agent

import (
	"testing"
)

func TestDefaultSystemPrompt(t *testing.T) {
	got := DefaultSystemPrompt()
	if got == "" {
		t.Error("expected non-empty default system prompt")
	}
}

func TestDefaultAgentSoul(t *testing.T) {
	got := DefaultAgentSoul()
	if got == "" {
		t.Error("expected non-empty default agent soul")
	}
}
