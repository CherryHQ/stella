package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/credentials"
)

// GetOAuthProviderConfig handles GET /api/admin/oauth-providers/{id}/config.
func (s *Server) GetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	cfg, err := s.credSvc.GetOAuthProviderConfig(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPIProviderConfig(cfg))
}

// DeleteOAuthProviderConfig handles DELETE /api/admin/oauth-providers/{id}/config.
func (s *Server) DeleteOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if err := s.credSvc.DeleteOAuthProviderConfig(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetOAuthProviderConfig handles PUT /api/admin/oauth-providers/{id}/config.
func (s *Server) SetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	var body apiserver.SetOAuthProviderConfigJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ClientId == "" {
		writeError(w, http.StatusBadRequest, "client_id is required")
		return
	}
	err := s.credSvc.SetOAuthProviderConfig(r.Context(), credentials.OAuthProviderConfig{
		ProviderID:   id,
		ClientID:     body.ClientId,
		ClientSecret: body.ClientSecret,
		RedirectURL:  stringVal(body.RedirectUrl),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg, err := s.credSvc.GetOAuthProviderConfig(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPIProviderConfig(cfg))
}

func toAPIProviderConfig(cfg credentials.OAuthProviderConfig) apiserver.OAuthProviderConfig {
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
