package server

import (
	"regexp"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
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

// fillAgentDefaults populates empty system_prompt with the embedded default.
func fillAgentDefaults(a *config.Agent) {
	if a.SystemPrompt == "" {
		a.SystemPrompt = agent.DefaultSystemPrompt()
	}
}
