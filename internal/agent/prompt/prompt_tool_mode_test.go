package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
)

// TestSystemPromptDescribesOnlyTheActiveToolStrategy pins the default path: a
// native session is never told about a `code` tool it will not be offered.
func TestSystemPromptDescribesOnlyTheActiveToolStrategy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		codeMode bool
		wantCode bool
	}{
		{name: "native", codeMode: false, wantCode: false},
		{name: "code", codeMode: true, wantCode: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			system := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
				SystemPrompt: "base",
				CodeMode:     tt.codeMode,
			})
			if got := strings.Contains(system, "`code`"); got != tt.wantCode {
				t.Fatalf("prompt mentions the code tool = %v, want %v", got, tt.wantCode)
			}
			if !strings.Contains(system, "`bash`") {
				t.Fatal("prompt lost the bash guidance")
			}
			if tt.codeMode {
				for _, guidance := range []string{
					"If it is not listed, use the directly listed tools",
					"do not wrap that standalone call in `code`",
					"If the exact name is known but its input schema is not, call `tools.describe(name)` directly",
					"Do not use skill search as a substitute for tool discovery",
				} {
					if !strings.Contains(system, guidance) {
						t.Fatalf("code prompt lost routing guidance %q", guidance)
					}
				}
			}
		})
	}
}
