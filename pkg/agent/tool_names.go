package agent

import (
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
)

func normalizeGeneratedToolCallName(name string, definitions []ai.ToolDefinition) string {
	prefix := ""
	for _, candidate := range []string{"functions_", "function_"} {
		if strings.HasPrefix(name, candidate) {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		return name
	}

	rest := strings.TrimPrefix(name, prefix)
	idx := strings.LastIndex(rest, "_")
	if idx <= 0 || !allDigits(rest[idx+1:]) {
		return name
	}

	candidate := rest[:idx]
	for _, def := range definitions {
		if def.Name == candidate {
			return candidate
		}
	}
	return name
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
