package channel

import "strings"

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
