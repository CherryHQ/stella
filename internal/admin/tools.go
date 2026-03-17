package admin

import (
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/agent/tool"
	memorytool "github.com/vaayne/anna/internal/memory/tool"
	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/internal/toolspec"
)

// toolJSON is the JSON representation of a tool definition.
type toolJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Category    string         `json:"category"` // "builtin", "shared", or "extra"
}

func defToJSON(def toolspec.Definition, category string) toolJSON {
	return toolJSON{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.InputSchema,
		Category:    category,
	}
}

func (s *Server) listAgentTools(w http.ResponseWriter, r *http.Request) {
	var tools []toolJSON

	// Built-in tools from registry (Read, Bash, Edit, Write, WebFetch).
	reg := tool.NewRegistry("")
	for _, def := range reg.Definitions() {
		tools = append(tools, defToJSON(def, "builtin"))
	}

	// Delegate tool (always present).
	tools = append(tools, defToJSON(tool.DelegateDefinition(), "builtin"))

	// Shared tools (scheduler, memory, skills).
	for _, def := range sharedToolDefinitions() {
		tools = append(tools, defToJSON(def, "shared"))
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

// sharedToolDefinitions returns the canonical definitions from each tool
// package. No runtime dependencies needed — schemas are static.
func sharedToolDefinitions() []toolspec.Definition {
	return []toolspec.Definition{
		scheduler.SchedulerDefinition(),
		memorytool.MemoryDefinition(),
		skills.SkillsDefinition(),
	}
}
