package channel

import (
	"fmt"
	"strings"
)

// ModelOption represents a selectable provider/model combination.
type ModelOption struct {
	Provider string
	Model    string
}

// IndexedModel pairs a ModelOption with its 1-based global index.
type IndexedModel struct {
	ModelOption
	GlobalIdx int
}

// ParseModelArgs parses /model arguments as a query string.
// Returns empty string when no arguments are provided.
func ParseModelArgs(args string) string {
	return strings.TrimSpace(args)
}

// IndexModels wraps a full model list with sequential 1-based indices.
func IndexModels(models []ModelOption) []IndexedModel {
	out := make([]IndexedModel, len(models))
	for i, m := range models {
		out[i] = IndexedModel{ModelOption: m, GlobalIdx: i + 1}
	}
	return out
}

// FilterModels returns indexed models matching the query, preserving
// their 1-based global indices from the full list.
func FilterModels(models []ModelOption, query string) []IndexedModel {
	query = strings.ToLower(query)
	var out []IndexedModel
	for i, m := range models {
		label := strings.ToLower(m.Provider + "/" + m.Model)
		if strings.Contains(label, query) {
			out = append(out, IndexedModel{ModelOption: m, GlobalIdx: i + 1})
		}
	}
	return out
}

// FindModelByName looks up a model by "provider/model" name (case-insensitive).
// Returns the model and true if found, zero value and false otherwise.
func FindModelByName(models []ModelOption, name string) (ModelOption, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, m := range models {
		if strings.ToLower(m.Provider+"/"+m.Model) == name {
			return m, true
		}
	}
	return ModelOption{}, false
}

// FormatModelList builds a text-based model list with bullet points.
func FormatModelList(models []IndexedModel, query string) string {
	var sb strings.Builder
	sb.WriteString("Available models")
	if query != "" {
		fmt.Fprintf(&sb, " (filter: %q)", query)
	}
	sb.WriteString(":\n\n")
	for _, m := range models {
		fmt.Fprintf(&sb, "• %s/%s\n", m.Provider, m.Model)
	}
	sb.WriteString("\nUse /model <provider/model> to switch.")
	return sb.String()
}
