package agent

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestNormalizeGeneratedToolCallName(t *testing.T) {
	definitions := []ai.ToolDefinition{
		{Name: "bash"},
		{Name: "read_file"},
	}

	tests := []struct {
		name string
		want string
	}{
		{name: "bash", want: "bash"},
		{name: "functions_bash_7", want: "bash"},
		{name: "function_bash_7", want: "bash"},
		{name: "functions_read_file_12", want: "read_file"},
		{name: "functions_missing_1", want: "functions_missing_1"},
		{name: "functions_bash_x", want: "functions_bash_x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGeneratedToolCallName(tt.name, definitions)
			if got != tt.want {
				t.Fatalf("normalizeGeneratedToolCallName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
