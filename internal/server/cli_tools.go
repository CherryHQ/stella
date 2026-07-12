package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/api/types"
)

// SearchCliToolRegistry searches the mise tool registry so the UI can add a CLI
// tool by picking a name instead of hand-writing its mise backend key.
func (s *Server) SearchCliToolRegistry(w http.ResponseWriter, r *http.Request, params apiserver.SearchCliToolRegistryParams) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	limit := 30
	if params.Limit != nil {
		limit = *params.Limit
	}
	tools, err := access.SearchCliToolRegistry(r.Context(), params.Q, limit)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	items := make([]types.CliToolRegistryItem, 0, len(tools))
	for _, t := range tools {
		name := t.Name
		backends := t.Backends
		items = append(items, types.CliToolRegistryItem{Name: &name, Backends: &backends})
	}
	writeData(w, http.StatusOK, types.CliToolRegistryResponse{Tools: items})
}

// GetCliToolLatest resolves the latest installable version for a mise tool key,
// letting the UI pin a tool to its current latest without the admin running mise.
func (s *Server) GetCliToolLatest(w http.ResponseWriter, r *http.Request, params apiserver.GetCliToolLatestParams) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	version, err := access.CliToolLatest(r.Context(), params.Tool)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, types.CliToolLatestResponse{Version: version})
}
