package server

import (
	"net/http"
	"strings"

	"github.com/vaayne/anna/internal/credentials"
)

// flowStatusJSON is the wire representation of an in-flight OAuth flow.
type flowStatusJSON struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flow_id"`
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresAt       string `json:"expires_at"`
	State           string `json:"state"`
}

func toFlowStatusJSON(fs credentials.FlowStatus) flowStatusJSON {
	return flowStatusJSON{
		Provider:        fs.Provider,
		FlowID:          fs.FlowID,
		VerificationURI: fs.VerificationURI,
		UserCode:        fs.UserCode,
		ExpiresAt:       fs.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		State:           fs.State,
	}
}

// ListOAuthProviders handles GET /api/auth/profile/oauth/providers.
func (s *Server) ListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	providers := s.credSvc.GetProviderStatuses(r.Context(), info.UserID)
	writeData(w, http.StatusOK, providers)
}

// StartOAuthFlow handles POST /api/auth/profile/oauth/{provider}/start.
func (s *Server) StartOAuthFlow(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	status, err := s.credSvc.StartFlowWithOrigin(r.Context(), info.UserID, provider, requestOrigin(r))
	if err != nil {
		s.log.Error("start oauth flow", "provider", provider, "user_id", info.UserID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// PollOAuthFlow handles GET /api/auth/profile/oauth/{provider}/status/{flowID}.
func (s *Server) PollOAuthFlow(w http.ResponseWriter, r *http.Request, provider string, flowID string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	status, _, err := s.credSvc.PollFlow(r.Context(), info.UserID, provider, flowID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// GetOAuthConnected handles GET /api/auth/profile/oauth/{provider}/connected.
func (s *Server) GetOAuthConnected(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	statuses := s.credSvc.GetProviderStatuses(r.Context(), info.UserID)

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

// DisconnectOAuth handles DELETE /api/auth/profile/oauth/{provider}.
func (s *Server) DisconnectOAuth(w http.ResponseWriter, r *http.Request, provider string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if err := s.credSvc.Disconnect(r.Context(), info.UserID, provider); err != nil {
		s.log.Error("disconnect oauth", "provider", provider, "user_id", info.UserID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
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
