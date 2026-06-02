package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/vault"
)

// ListAuthProviders handles GET /api/auth/providers.
// Returns the list of configured OIDC providers so the frontend can build
// login buttons. Public endpoint — no authentication required.
func (s *Server) ListAuthProviders(w http.ResponseWriter, r *http.Request) {
	out := apitypes.OIDCProviderList{Providers: []apitypes.OIDCProvider{}}
	for _, p := range s.authProviders {
		prov := apitypes.OIDCProvider{
			Name:     p.Name(),
			LoginUrl: fmt.Sprintf("/auth/login/%s", p.Name()),
		}
		if p.Name() == "local" && s.localAuth != nil && s.localAuth.AllowsRegistration(r.Context()) {
			regURL := "/signup"
			prov.RegisterUrl = &regURL
		}
		out.Providers = append(out.Providers, prov)
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) LoginLocal(w http.ResponseWriter, r *http.Request) {
	if s.localAuth == nil || s.stateMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "local authentication is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var body apiserver.LoginLocalJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ip := clientIP(r)
	email := string(body.Email)
	if err := s.rateLimiter.CheckIP(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if err := s.rateLimiter.CheckUsername(email); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	state, ok := s.startLocalAuthFlow(w, r)
	if !ok {
		return
	}
	redirectURL, err := s.localAuth.Login(r.Context(), local.LoginInput{
		Email:    email,
		Password: body.Password,
		State:    state,
	})
	if err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		if errors.Is(err, local.ErrInvalidLogin) {
			s.rateLimiter.RecordLoginFailure(email)
		}
		s.writeLocalAuthError(w, err)
		return
	}
	s.rateLimiter.RecordLoginSuccess(email)
	writeData(w, http.StatusOK, apitypes.LocalAuthRedirect{RedirectUrl: redirectURL})
}

func (s *Server) RegisterLocal(w http.ResponseWriter, r *http.Request) {
	if s.localAuth == nil || s.stateMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "local authentication is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var body apiserver.RegisterLocalJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ip := clientIP(r)
	if err := s.rateLimiter.CheckIP(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	state, ok := s.startLocalAuthFlow(w, r)
	if !ok {
		return
	}
	redirectURL, err := s.localAuth.Register(r.Context(), local.RegisterInput{
		Name:            body.Name,
		Email:           string(body.Email),
		Password:        body.Password,
		ConfirmPassword: body.ConfirmPassword,
		State:           state,
	})
	if err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		s.writeLocalAuthError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.LocalAuthRedirect{RedirectUrl: redirectURL})
}

func (s *Server) startLocalAuthFlow(w http.ResponseWriter, r *http.Request) (auth.AuthState, bool) {
	payload, err := s.stateMgr.Generate()
	if err != nil {
		slog.Error("local auth: generate state", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return auth.AuthState{}, false
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	if err := s.stateMgr.SetCookie(w, payload, secure); err != nil {
		slog.Error("local auth: set state cookie", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return auth.AuthState{}, false
	}
	return auth.AuthState{State: payload.State, CodeVerifier: payload.CodeVerifier, ProviderName: "local"}, true
}

func (s *Server) writeLocalAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, local.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, local.ErrInvalidLogin):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
	case errors.Is(err, local.ErrAccountDisabled):
		writeError(w, http.StatusUnauthorized, "account is disabled")
	case errors.Is(err, local.ErrRegistrationDisabled):
		writeError(w, http.StatusForbidden, "registration is disabled")
	case errors.Is(err, local.ErrEmailExists):
		writeError(w, http.StatusConflict, "an account with this email already exists")
	default:
		s.writeInternalError(w, err)
	}
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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

	// Local provider: redirect to the SPA login page directly. The SPA
	// generates its own OIDC state via the JSON API, so setting one here
	// would only be overwritten.
	if providerName == "local" {
		dest := "/login"
		if r.URL.Query().Get("mode") == "register" {
			dest = "/signup"
		}
		http.Redirect(w, r, dest, http.StatusFound)
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

	if r.URL.Query().Get("mode") == "register" {
		loginURL += "&mode=register"
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

	// Ensure every user has an auto-generated API token (idempotent).
	if s.tokenSvc != nil {
		if err := s.tokenSvc.EnsureAutoToken(r.Context(), result.User.ID); err != nil {
			slog.Warn("oidc: ensure auto token failed", "user_id", result.User.ID, "error", err)
		}
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
