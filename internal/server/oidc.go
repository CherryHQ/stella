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
	if s.localAuth != nil {
		prov := apitypes.OIDCProvider{
			Name:     "local",
			LoginUrl: "/auth/login/local",
		}
		if s.localAuth.AllowsRegistration(r.Context()) {
			regURL := "/signup"
			prov.RegisterUrl = &regURL
		}
		out.Providers = append(out.Providers, prov)
	}
	for _, p := range s.authProviders {
		out.Providers = append(out.Providers, apitypes.OIDCProvider{
			Name:     p.Name(),
			LoginUrl: fmt.Sprintf("/auth/login/%s", p.Name()),
		})
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) LoginLocal(w http.ResponseWriter, r *http.Request) {
	if s.localAuth == nil || s.authSvc == nil || s.sessionMgr == nil {
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

	userID, err := s.localAuth.Login(r.Context(), local.LoginInput{
		Email:    email,
		Password: body.Password,
	})
	if err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		if errors.Is(err, local.ErrInvalidLogin) {
			s.rateLimiter.RecordLoginFailure(email)
		}
		s.writeLocalAuthError(w, err)
		return
	}
	result, err := s.authSvc.CreateSessionForUser(r.Context(), userID, s.sessionMgr)
	if err != nil {
		slog.Error("local auth: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	s.finalizeLogin(w, r, result)
	s.rateLimiter.RecordLoginSuccess(email)
	writeData(w, http.StatusOK, apitypes.LocalAuthRedirect{RedirectUrl: "/"})
}

func (s *Server) RegisterLocal(w http.ResponseWriter, r *http.Request) {
	if s.localAuth == nil || s.authSvc == nil || s.sessionMgr == nil {
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

	userID, err := s.localAuth.Register(r.Context(), local.RegisterInput{
		Name:            body.Name,
		Email:           string(body.Email),
		Password:        body.Password,
		ConfirmPassword: body.ConfirmPassword,
	})
	if err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		s.writeLocalAuthError(w, err)
		return
	}
	result, err := s.authSvc.CreateSessionForUser(r.Context(), userID, s.sessionMgr)
	if err != nil {
		slog.Error("local auth: create registration session", "error", err)
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	s.finalizeLogin(w, r, result)
	writeData(w, http.StatusOK, apitypes.LocalAuthRedirect{RedirectUrl: "/"})
}

func (s *Server) writeLocalAuthError(w http.ResponseWriter, err error) {
	switch {
	// ErrEmailExists is deliberately folded into the generic invalid-input
	// response so the endpoint never confirms whether an email is registered
	// (prevents account enumeration). The real reason is logged server-side.
	case errors.Is(err, local.ErrInvalidInput), errors.Is(err, local.ErrEmailExists):
		if errors.Is(err, local.ErrEmailExists) {
			slog.Info("local auth: registration rejected for existing email")
		}
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, local.ErrInvalidLogin):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
	case errors.Is(err, local.ErrAccountDisabled):
		writeError(w, http.StatusUnauthorized, "account is disabled")
	case errors.Is(err, local.ErrRegistrationDisabled):
		writeError(w, http.StatusForbidden, "registration is disabled")
	case errors.Is(err, local.ErrEmailNotAllowed):
		writeError(w, http.StatusForbidden, "this email domain is not allowed to register")
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
	// Local provider: redirect to the SPA login page directly. The SPA submits
	// credentials through the JSON API and does not use an OIDC redirect flow.
	if providerName == "local" && s.localAuth != nil {
		dest := "/login"
		if r.URL.Query().Get("mode") == "register" {
			dest = "/signup"
		}
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}

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

	s.finalizeLogin(w, r, result)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) finalizeLogin(w http.ResponseWriter, r *http.Request, result auth.OIDCLoginResult) {
	// Ensure every user has an auto-generated API token (idempotent).
	if s.tokenSvc != nil {
		if err := s.tokenSvc.EnsureAutoToken(r.Context(), result.User.ID); err != nil {
			slog.Warn("auth: ensure auto token failed", "user_id", result.User.ID, "error", err)
		}
	}

	// Provision vault age keys when the user doesn't have them yet.
	if result.User.AgePublicKey == "" && s.vaultRecipient != nil {
		pubKey, encPrivKey, err := vault.GenerateUserKeys(s.vaultRecipient)
		if err != nil {
			slog.Warn("auth: generate age keys failed", "user_id", result.User.ID, "error", err)
		} else if err := s.authSvc.UpdateUserAgeKeys(r.Context(), result.User.ID, pubKey, encPrivKey); err != nil {
			slog.Warn("auth: store age keys failed", "user_id", result.User.ID, "error", err)
		}
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	s.sessionMgr.SetCookie(w, result.SessionToken, secure)
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
