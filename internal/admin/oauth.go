package admin

import (
	"net/http"

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

// listOAuthProviders handles GET /api/auth/profile/oauth/providers.
func (s *Server) listOAuthProviders(w http.ResponseWriter, r *http.Request) {
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

// startOAuthFlow handles POST /api/auth/profile/oauth/{provider}/start.
func (s *Server) startOAuthFlow(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	provider := r.PathValue("provider")
	status, err := s.credSvc.StartFlow(r.Context(), info.UserID, provider)
	if err != nil {
		s.log.Error("start oauth flow", "provider", provider, "user_id", info.UserID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// pollOAuthFlow handles GET /api/auth/profile/oauth/{provider}/status/{flowID}.
func (s *Server) pollOAuthFlow(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	provider := r.PathValue("provider")
	flowID := r.PathValue("flowID")

	status, _, err := s.credSvc.PollFlow(r.Context(), info.UserID, provider, flowID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toFlowStatusJSON(status))
}

// getOAuthConnected handles GET /api/auth/profile/oauth/{provider}/connected.
func (s *Server) getOAuthConnected(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	provider := r.PathValue("provider")
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

// disconnectOAuth handles DELETE /api/auth/profile/oauth/{provider}.
func (s *Server) disconnectOAuth(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	provider := r.PathValue("provider")
	if err := s.credSvc.Disconnect(r.Context(), info.UserID, provider); err != nil {
		s.log.Error("disconnect oauth", "provider", provider, "user_id", info.UserID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// oauthCallback handles GET /api/auth/profile/oauth/{provider}/callback.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		http.Error(w, "vault not configured", http.StatusServiceUnavailable)
		return
	}

	provider := r.PathValue("provider")
	code := r.URL.Query().Get("code")
	flowID := r.URL.Query().Get("state")
	if code == "" || flowID == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	flow, ok := s.credSvc.GetFlowForCallback(flowID)
	if !ok {
		http.Error(w, "unknown or expired flow", http.StatusBadRequest)
		return
	}

	if err := s.credSvc.CompleteAuthCodeFlow(r.Context(), provider, flowID, code); err != nil {
		s.log.Error("oauth callback complete", "provider", provider, "user_id", flow.UserID, "flow_id", flowID, "error", err)
		http.Error(w, "failed to complete authorization: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/credentials", http.StatusFound)
}
