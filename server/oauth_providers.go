package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/credentials"
)

// GetOAuthProviderConfig handles GET /api/admin/oauth-providers/{id}/config.
func (s *Server) GetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	cfg, err := s.credSvc.GetOAuthProviderConfig(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPIProviderConfig(cfg))
}

// SetOAuthProviderConfig handles PUT /api/admin/oauth-providers/{id}/config.
func (s *Server) SetOAuthProviderConfig(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	var body apiserver.SetOAuthProviderConfigJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	w.WriteHeader(http.StatusNoContent)
}

func toAPIProviderConfig(cfg credentials.OAuthProviderConfig) apiserver.OAuthProviderConfig {
	return apiserver.OAuthProviderConfig{
		ProviderId:   cfg.ProviderID,
		ClientId:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectUrl:  &cfg.RedirectURL,
	}
}

func stringVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
