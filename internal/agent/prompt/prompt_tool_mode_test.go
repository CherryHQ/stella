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
					"Native tools for standalone work",
					"Never wrap a standalone native call in `code`",
					"If the exact name is known but its schema is not, describe it directly",
					"Skill search finds behavior guides, not tools",
				} {
					if !strings.Contains(system, guidance) {
						t.Fatalf("code prompt lost routing guidance %q", guidance)
					}
				}
			}
		})
	}
}
