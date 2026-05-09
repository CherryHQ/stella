package server

import (
	"net/http"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/version"
)

func (s *Server) GetStatus(w http.ResponseWriter, r *http.Request) {
	resp := types.StatusResponse{
		Status:  "ok",
		Version: version.Version,
	}
	if version.Commit != "" {
		resp.Commit = &version.Commit
	}
	if version.BuildDate != "" {
		resp.BuildDate = &version.BuildDate
	}
	writeData(w, http.StatusOK, resp)
}
