package admin

import (
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/toolspec"
)

// toolJSON is the JSON representation of a tool definition.
type toolJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Category    string         `json:"category"` // "builtin", "shared", or "extra"
}

func (s *Server) listAgentTools(w http.ResponseWriter, r *http.Request) {
	var tools []toolJSON

	// Built-in tools from registry (Read, Bash, Edit, Write, WebFetch).
	reg := tool.NewRegistry("")
	for _, def := range reg.Definitions() {
		tools = append(tools, toolJSON{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
			Category:    "builtin",
		})
	}

	// Delegate tool (always present).
	tools = append(tools, toolJSON{
		Name:        "delegate",
		Description: "Delegate a sub-task to a parallel agent that has the same tools.",
		Category:    "builtin",
	})

	// Shared tools that are typically always present.
	for _, def := range sharedToolDefinitions() {
		tools = append(tools, toolJSON{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
			Category:    "shared",
		})
	}

	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Category != tools[j].Category {
			order := map[string]int{"builtin": 0, "shared": 1, "extra": 2}
			return order[tools[i].Category] < order[tools[j].Category]
		}
		return tools[i].Name < tools[j].Name
	})

	writeData(w, http.StatusOK, tools)
}

// sharedToolDefinitions returns definitions for shared tools that are
// typically always available (scheduler, memory, skills).
func sharedToolDefinitions() []toolspec.Definition {
	return []toolspec.Definition{
		{
			Name:        "scheduler",
			Description: "Manage scheduled jobs: create, list, update, delete, or toggle jobs.",
		},
		{
			Name:        "memory",
			Description: "Read and write persistent memory for the current user-agent pair.",
		},
		{
			Name:        "skills",
			Description: "Search, install, list, and remove skills from the ecosystem.",
		},
	}
}
