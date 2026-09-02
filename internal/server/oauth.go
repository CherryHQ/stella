package server

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/connections"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
)

// credAccess derives the trusted Authority for the authenticated caller and
// binds one connections use case to it. The user-facing OAuth capability is
// user-owned: bundles and flows are scoped to the captured user.
func (s *Server) credAccess(w http.ResponseWriter, r *http.Request) (*connections.Access, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	acc, err := s.credSvc.Access(authority)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	return acc, true
}

// flowStatusJSON is the wire representation of an in-flight OAuth flow.
type flowStatusJSON struct {
	Provider        string    `json:"provider"`
	FlowID          string    `json:"flow_id"`
	VerificationURI string    `json:"verification_uri"`
	UserCode        string    `json:"user_code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	State           string    `json:"state"`
	Error           string    `json:"error,omitempty"`
	RequestedScopes []string  `json:"requested_scopes,omitempty"`
}

func toFlowStatusJSON(fs connections.FlowStatus) flowStatusJSON {
	return flowStatusJSON{
		Provider:        fs.Provider,
		FlowID:          fs.FlowID,
		VerificationURI: fs.VerificationURI,
		UserCode:        fs.UserCode,
		ExpiresAt:       fs.ExpiresAt.UTC(),
		State:           fs.State,
		Error:           fs.Error,
		RequestedScopes: fs.RequestedScopes,
	}
}

// ListOAuthProviders handles GET /api/users/me/oauth/providers.
func (s *Server) ListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.credAccess(w, r)
	if !ok {
		return
	}
	providers, err := acc.Statuses(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	requiredBy := s.oauthProviderRequiredBy()
	for i := range providers {
		providers[i].RequiredBy = requiredBy[providers[i].Provider]
	}
	writeData(w, http.StatusOK, map[string]any{"providers": providers})
}

// oauthProviderRequiredBy maps each tool OAuth provider to the display names of
// enabled tools that depend on it, derived from the plugin manifest's
// session-env specs. The credentials page uses this to tell users which tool a
// connection unlocks, since login no longer carries tool scopes — a user must
// connect each tool provider explicitly.
func (s *Server) oauthProviderRequiredBy() map[string][]string {
	return oauthProviderRequiredBy(s.pluginHost)
}

func oauthProviderRequiredBy(host *pluginhost.Host) map[string][]string {
	if host == nil {
		return nil
	}
	displayByID := make(map[string]string)
	for _, p := range host.ListRegisteredPlugins() {
		name := p.DisplayName
		if name == "" {
			name = p.Name
		}
		displayByID[p.ID] = name
	}
	out := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, spec := range host.AllSessionEnvSpecs() {
		if spec.OAuthProviderID == "" {
			continue
		}
		name := displayByID[spec.PluginID]
		if name == "" {
			continue
		}
		if seen[spec.OAuthProviderID] == nil {
			seen[spec.OAuthProviderID] = make(map[string]struct{})
		}
		if _, dup := seen[spec.OAuthProviderID][name]; dup {
			continue
		}
		seen[spec.OAuthProviderID][name] = struct{}{}
		out[spec.OAuthProviderID] = append(out[spec.OAuthProviderID], name)
	}
	return out
}

// StartOAuthFlow handles POST /api/users/me/oauth/{provider}/start.
func (s *Server) StartOAuthFlow(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	acc, ok := s.credAccess(w, r)
	if !ok {
		return
	}

	// Use the explicit Origin header (sent by browsers) so the redirect URI
	// matches the Web UI host. CLI/curl requests omit Origin; passing "" lets
	// the credential service fall back to the configured base URL.
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	status, err := acc.StartFlow(r.Context(), provider, body.Scopes, origin)
	if err != nil {
		s.log.Error("start oauth flow", "provider", provider, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// PollOAuthFlow handles GET /api/users/me/oauth/{provider}/status/{flowID}.
func (s *Server) PollOAuthFlow(w http.ResponseWriter, r *http.Request, provider string, flowID string) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	acc, ok := s.credAccess(w, r)
	if !ok {
		return
	}

	status, _, err := acc.PollFlow(r.Context(), provider, flowID)
	if err != nil {
		s.log.Error("poll oauth flow", "provider", provider, "flow_id", flowID, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// GetOAuthConnected handles GET /api/users/me/oauth/{provider}/connected.
func (s *Server) GetOAuthConnected(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	acc, ok := s.credAccess(w, r)
	if !ok {
		return
	}

	statuses, err := acc.Statuses(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	type connectedResp struct {
		Connected bool   `json:"connected"`
		Username  string `json:"username,omitempty"`
	}

	for _, ps := range statuses {
		if ps.Provider == provider {
			writeData(w, http.StatusOK, connectedResp{Connected: ps.Connected, Username: ps.Username})
			return
		}
	}
	writeError(w, http.StatusBadRequest, "unsupported provider: "+provider)
}

// DisconnectOAuth handles DELETE /api/users/me/oauth/{provider}.
func (s *Server) DisconnectOAuth(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	acc, ok := s.credAccess(w, r)
	if !ok {
		return
	}

	if err := acc.Disconnect(r.Context(), provider); err != nil {
		s.log.Error("disconnect oauth", "provider", provider, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requestOrigin(r *http.Request) string {
	if origin := strings.TrimRight(r.Header.Get("Origin"), "/"); origin != "" {
		return origin
	}

	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
