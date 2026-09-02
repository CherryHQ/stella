package server

import (
	"regexp"
	"strings"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/platform/config"
)

// slugify converts a name to a URL-safe agent ID.
// "My Cool Agent" -> "my-cool-agent"
var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agent"
	}
	return s
}

// fillAgentDefaults populates empty system_prompt with the embedded default,
// named after this agent so it does not introduce itself as somebody else.
func fillAgentDefaults(a *config.Agent) {
	if a.SystemPrompt == "" {
		a.SystemPrompt = prompt.DefaultSystemPromptFor(a.Name)
	}
}
