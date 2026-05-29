package server

import (
	"fmt"
	"log/slog"
	"net/http"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/vault"
)

// ListAuthProviders handles GET /api/auth/providers.
// Returns the list of configured OIDC providers so the frontend can build
// login buttons. Public endpoint — no authentication required.
func (s *Server) ListAuthProviders(w http.ResponseWriter, r *http.Request) {
	out := apitypes.OIDCProviderList{Items: []apitypes.OIDCProvider{}}
	for _, p := range s.authProviders {
		prov := apitypes.OIDCProvider{
			Name:     p.Name(),
			LoginUrl: fmt.Sprintf("/auth/login/%s", p.Name()),
		}
		if p.Name() == "local" {
			regURL := fmt.Sprintf("/auth/login/%s?mode=register", p.Name())
			prov.RegisterUrl = &regURL
		}
		out.Items = append(out.Items, prov)
	}
	writeData(w, http.StatusOK, out)
}

// handleOIDCLogin handles GET /auth/login/{provider}.
// Generates PKCE + state, sets a signed cookie, and redirects the browser to
// the IdP authorization endpoint.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.stateMgr == nil || len(s.authProviders) == 0 {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)
		return
	}

	providerName := r.PathValue("provider")
	provider := s.findProvider(providerName)
	if provider == nil {
		slog.Warn("oidc: unknown provider", "provider", providerName, "available", s.providerNames())
		http.NotFound(w, r)
		return
	}

	payload, err := s.stateMgr.Generate()
	if err != nil {
		slog.Error("oidc: generate state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	if err := s.stateMgr.SetCookie(w, payload, secure); err != nil {
		slog.Error("oidc: set state cookie", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	state := auth.AuthState{
		State:        payload.State,
		CodeVerifier: payload.CodeVerifier,
		ProviderName: providerName,
	}
	loginURL, err := provider.LoginURL(r.Context(), state)
	if err != nil {
		slog.Error("oidc: build login url", "provider", providerName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if mode := r.URL.Query().Get("mode"); mode != "" {
		loginURL += "&mode=" + mode
	}

	http.Redirect(w, r, loginURL, http.StatusFound)
}

// handleOIDCCallback handles GET /auth/callback/{provider}.
// Validates the state cookie, exchanges the authorization code, upserts the
// user, creates a session, and redirects the browser to the home page.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.stateMgr == nil || s.authSvc == nil || s.sessionMgr == nil {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)
		return
	}

	providerName := r.PathValue("provider")
	provider := s.findProvider(providerName)
	if provider == nil {
		http.NotFound(w, r)
		return
	}

	queryState := r.URL.Query().Get("state")
	payload, err := s.stateMgr.ValidateAndClear(w, r, queryState)
	if err != nil {
		slog.Warn("oidc: state validation failed", "provider", providerName, "error", err)
		http.Error(w, "invalid or expired login session", http.StatusBadRequest)
		return
	}

	state := auth.AuthState{
		State:        payload.State,
		CodeVerifier: payload.CodeVerifier,
		ProviderName: providerName,
	}
	identity, err := provider.HandleCallback(r.Context(), r, state)
	if err != nil {
		slog.Warn("oidc: callback exchange failed", "provider", providerName, "error", err)
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	result, err := s.authSvc.ProcessOIDCLogin(r.Context(), *identity, s.sessionMgr)
	if err != nil {
		slog.Error("oidc: process login", "provider", providerName, "error", err)
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}

	// Provision vault age keys when the user doesn't have them yet.
	if result.User.AgePublicKey == "" && s.vaultRecipient != nil {
		pubKey, encPrivKey, err := vault.GenerateUserKeys(s.vaultRecipient)
		if err != nil {
			slog.Warn("oidc: generate age keys failed", "user_id", result.User.ID, "error", err)
		} else if err := s.authSvc.UpdateUserAgeKeys(r.Context(), result.User.ID, pubKey, encPrivKey); err != nil {
			slog.Warn("oidc: store age keys failed", "user_id", result.User.ID, "error", err)
		}
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	s.sessionMgr.SetCookie(w, result.SessionToken, secure)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) findProvider(name string) auth.AuthProvider {
	for _, p := range s.authProviders {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (s *Server) providerNames() []string {
	names := make([]string, len(s.authProviders))
	for i, p := range s.authProviders {
		names[i] = p.Name()
	}
	return names
}
