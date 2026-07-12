package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/connections"
)

// GetOAuthProviderConfig handles GET /api/admin/oauth-providers/{id}/config.
func (s *Server) GetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	cfg, err := access.GetOAuthProviderConfig(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIProviderConfig(cfg))
}

// DeleteOAuthProviderConfig handles DELETE /api/admin/oauth-providers/{id}/config.
func (s *Server) DeleteOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteOAuthProviderConfig(r.Context(), id); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetOAuthProviderConfig handles PUT /api/admin/oauth-providers/{id}/config.
func (s *Server) SetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body apiserver.SetOAuthProviderConfigJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.ClientId == "" {
		writeError(w, http.StatusBadRequest, "client_id is required")
		return
	}
	cfg, err := access.SetOAuthProviderConfig(r.Context(), connections.OAuthProviderConfig{
		ProviderID:   id,
		ClientID:     body.ClientId,
		ClientSecret: body.ClientSecret,
		RedirectURL:  stringVal(body.RedirectUrl),
	})
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAPIProviderConfig(cfg))
}

func toAPIProviderConfig(cfg connections.OAuthProviderConfig) apiserver.OAuthProviderConfig {
	out := apiserver.OAuthProviderConfig{
		ProviderId:   cfg.ProviderID,
		ClientId:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	}
	if cfg.RedirectURL != "" {
		out.RedirectUrl = &cfg.RedirectURL
	}
	return out
}

func stringVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
