package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
)

func (s *Server) ListArtifactShares(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func (s *Server) CreateArtifactShare(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func (s *Server) RevokeArtifactShare(w http.ResponseWriter, r *http.Request, id string) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func (s *Server) GetPublicArtifactShare(w http.ResponseWriter, r *http.Request, token string) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

func (s *Server) GetPublicArtifactShareContent(w http.ResponseWriter, r *http.Request, token string) {
	writeError(w, http.StatusNotImplemented, "artifact sharing is not implemented")
}

var _ apiserver.ServerInterface = (*Server)(nil)
