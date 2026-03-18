package admin

import (
	"net/http"

	"github.com/vaayne/anna/internal/config"
)

// listCachedModels returns models from the local cache file.
// No provider API calls — reads only from ~/.anna/cache/models.json.
func (s *Server) listCachedModels(w http.ResponseWriter, r *http.Request) {
	cache, err := config.LoadModelsCache()
	if err != nil {
		// No cache yet — return empty list, not an error.
		writeData(w, http.StatusOK, []any{})
		return
	}
	writeData(w, http.StatusOK, cache.Models)
}
