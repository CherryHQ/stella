package admin

import (
	"net/http"

	"github.com/vaayne/anna/internal/config"
)

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, channels)
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	ch, err := s.store.GetChannel(r.Context(), platform)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeData(w, http.StatusOK, ch)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	var ch config.Channel
	if err := decodeJSON(r, &ch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	ch.ID = platform
	if err := s.store.UpsertChannel(r.Context(), ch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ch.Enabled {
		s.stopChannel(platform)
	}
	writeData(w, http.StatusOK, ch)
}
