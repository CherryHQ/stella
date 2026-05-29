package server

import "net/http"

// Invite endpoints are stubs — multi-org invitations have been removed.

func (s *Server) ListInvites(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, []any{})
}

func (s *Server) CreateInvite(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, "invitations are not supported")
}

func (s *Server) RevokeInvite(w http.ResponseWriter, _ *http.Request, _ string) {
	writeError(w, http.StatusGone, "invitations are not supported")
}

func (s *Server) AcceptInvite(w http.ResponseWriter, _ *http.Request, _ string) {
	writeError(w, http.StatusGone, "invitations are not supported")
}

func (s *Server) GetInviteInfo(w http.ResponseWriter, _ *http.Request, _ string) {
	writeError(w, http.StatusGone, "invitations are not supported")
}
