package admin

import (
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/pkg/memory"
	pkgtools "github.com/vaayne/anna/pkg/tools"
	agenttool "github.com/vaayne/anna/plugins/tools/agent"
	"github.com/vaayne/anna/plugins/tools/bash"
	"github.com/vaayne/anna/plugins/tools/edit"
	"github.com/vaayne/anna/plugins/tools/read"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
	"github.com/vaayne/anna/plugins/tools/write"
)

// toolJSON is the JSON representation of a tool definition.
type toolJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Category    string         `json:"category"` // "builtin" or "extra"
}

func defToJSON(def pkgtools.Definition, category string) toolJSON {
	return toolJSON{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.InputSchema,
		Category:    category,
	}
}

func (s *Server) listAgentTools(w http.ResponseWriter, r *http.Request) {
	var tools []toolJSON

	// Built-in tools (Read, Bash, Edit, Write).
	builtinTools := []pkgtools.Tool{
		&read.ReadTool{},
		bash.NewBashTool("", ""),
		&edit.EditTool{},
		&write.WriteTool{},
	}
	for _, t := range builtinTools {
		tools = append(tools, defToJSON(t.Definition(), "builtin"))
	}

	// Agent tool (always present).
	tools = append(tools, defToJSON(agenttool.AgentDefinition(nil), "builtin"))

	// Builtin tools (scheduler, memory, skills).
	for _, def := range s.builtinToolDefinitions() {
		tools = append(tools, defToJSON(def, "builtin"))
	}

	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Category != tools[j].Category {
			order := map[string]int{"builtin": 0, "extra": 1}
			return order[tools[i].Category] < order[tools[j].Category]
		}
		return tools[i].Name < tools[j].Name
	})

	writeData(w, http.StatusOK, tools)
}

// builtinToolDefinitions returns the canonical definitions from each tool
// package. The memory tool definition is built dynamically from the provider.
func (s *Server) builtinToolDefinitions() []pkgtools.Definition {
	defs := []pkgtools.Definition{
		scheduler.SchedulerDefinition(),
		skillstool.SkillsDefinition(),
	}
	if s.mem != nil {
		defs = append(defs, memory.BuildTool(s.mem).Definition())
	}
	return defs
}
