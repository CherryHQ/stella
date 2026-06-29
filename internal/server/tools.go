package server

import (
	"net/http"
	"sort"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/tools"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
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
	var items []toolJSON

	for _, def := range tools.Definitions() {
		items = append(items, defToJSON(def, "builtin"))
	}

	// Delegate tool (always present).
	items = append(items, defToJSON(delegatetool.DelegateDefinition(nil), "builtin"))

	// Builtin tools (memory, skills).
	for _, def := range s.builtinToolDefinitions() {
		items = append(items, defToJSON(def, "builtin"))
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			order := map[string]int{"builtin": 0, "extra": 1}
			return order[items[i].Category] < order[items[j].Category]
		}
		return items[i].Name < items[j].Name
	})

	writeData(w, http.StatusOK, map[string]any{"tools": items})
}

// builtinToolDefinitions returns the canonical definitions from each tool
// package. The memory tool definition is built dynamically from the provider.
func (s *Server) builtinToolDefinitions() []pkgtools.Definition {
	var defs []pkgtools.Definition
	if s.mem != nil {
		defs = append(defs, memory.BuildTool(s.mem, memory.WithSessionReadOnlyWrites()).Definition())
	}
	return defs
}
