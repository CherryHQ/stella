package server

import (
	"errors"
	"net/http"
	"net/url"

	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"
)

// handleOAuthCallback is the single public OAuth callback route. Durable flow
// state distinguishes connection and MCP grants; a state that is not a client
// flow falls through to the cookie-bound OIDC login callback.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state != "" && s.credSvc != nil {
		flow, err := s.credSvc.GetDurableFlowForCallback(r.Context(), state)
		switch {
		case err == nil:
			s.handleOAuthClientFlow(w, r, flow)
			return
		case !errors.Is(err, credoauth.ErrFlowNotFound):
			s.log.Error("oauth callback flow lookup", "flow_id", state, "error", err)
			http.Error(w, "oauth callback unavailable", http.StatusServiceUnavailable)
			return
		}

		// Unit-only connection services can still use the in-memory broker. The
		// production composition always has the durable store.
		if flow, ok := s.credSvc.GetFlowForCallback(state); ok {
			provider := r.PathValue("provider")
			if string(flow.Provider) != provider {
				http.Error(w, "invalid oauth callback", http.StatusBadRequest)
				return
			}
			if err := s.credSvc.CompleteAuthCodeFlowWithOrigin(r.Context(), provider, state, r.URL.Query().Get("code"), requestOrigin(r)); err != nil {
				s.log.Error("oauth callback complete", "provider", provider, "user_id", flow.UserID, "flow_id", state, "error", err)
				s.writeInternalError(w, err)
				return
			}
			http.Redirect(w, r, "/settings/credentials", http.StatusFound)
			return
		}
	}

	s.handleOIDCCallback(w, r)
}

func (s *Server) handleOAuthClientFlow(w http.ResponseWriter, r *http.Request, flow credoauth.DurableFlow) {
	code := r.URL.Query().Get("code")
	if code == "" || flow.ID == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	switch flow.TargetKind {
	case "connection":
		if r.PathValue("provider") != flow.TargetID {
			http.Error(w, "invalid oauth callback", http.StatusBadRequest)
			return
		}
		if err := s.credSvc.CompleteAuthCodeFlow(r.Context(), flow.TargetID, flow.ID, code); err != nil {
			s.log.Error("oauth connection callback", "provider", flow.TargetID, "user_id", flow.UserID, "flow_id", flow.ID, "error", err)
			s.writeInternalError(w, err)
			return
		}
		http.Redirect(w, r, "/settings/credentials", http.StatusFound)
	case "mcp":
		if r.PathValue("provider") != "mcp" || s.mcpSvc == nil {
			http.Error(w, "invalid oauth callback", http.StatusBadRequest)
			return
		}
		reg, err := s.mcpSvc.CompleteOAuth(r.Context(), flow.ID, code)
		if err != nil {
			s.log.Warn("mcp oauth callback failed", "error", err)
			http.Redirect(w, r, "/settings/mcp?oauth_error="+mcpOAuthErrorSlug(err), http.StatusFound)
			return
		}
		http.Redirect(w, r, "/settings/mcp?connected="+url.QueryEscape(reg.ID), http.StatusFound)
	default:
		http.Error(w, "invalid oauth callback", http.StatusBadRequest)
	}
}
