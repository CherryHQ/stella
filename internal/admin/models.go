package admin

import (
	"net/http"

	"github.com/vaayne/anna/internal/config"
)

// listCachedModels returns models from the local cache file, filtered to only
// include models whose provider plugin is currently enabled.
// No provider API calls — reads only from ~/.anna/cache/models.json.
func (s *Server) listCachedModels(w http.ResponseWriter, r *http.Request) {
	cache, err := config.LoadModelsCache()
	if err != nil {
		// No cache yet — return empty list, not an error.
		writeData(w, http.StatusOK, []any{})
		return
	}

	plugins, err := s.store.ListPluginsByKind(r.Context(), config.PluginKindProvider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list providers: "+err.Error())
		return
	}
	enabled := make(map[string]bool, len(plugins))
	for _, p := range plugins {
		if p.Enabled {
			enabled[p.Name] = true
		}
	}

	filtered := make([]config.CachedModel, 0, len(cache.Models))
	for _, m := range cache.Models {
		if enabled[m.Provider] {
			filtered = append(filtered, m)
		}
	}
	writeData(w, http.StatusOK, filtered)
}
