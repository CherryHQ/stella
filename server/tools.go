package server

import (
	"net/http"
	"sort"

	coretools "github.com/CherryHQ/stella/internal/tools"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
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

func (s *Server) ListTools(w http.ResponseWriter, r *http.Request) {
	var tools []toolJSON

	for _, def := range coretools.Definitions() {
		tools = append(tools, defToJSON(def, "builtin"))
	}

	// Delegate tool (always present).
	tools = append(tools, defToJSON(delegatetool.DelegateDefinition(nil), "builtin"))

	// Builtin tools (memory, skills).
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
	var defs []pkgtools.Definition
	if s.mem != nil {
		defs = append(defs, memory.BuildTool(s.mem).Definition())
	}
	return defs
}
