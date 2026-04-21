package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vaayne/anna/internal/oauthcli"
)

const (
	pluginIDGitHub = "auth/github"
	pluginIDLark   = "auth/lark"
)

// larkCallbackPath is the path Lark redirects back to after user authorization.
// It must match the redirect_uri configured in the Lark app.
const larkCallbackPath = "/api/auth/profile/oauth/lark/callback"

// getGitHubBroker returns the cached GitHubBroker, lazily constructing it from
// the current plugin config. Returns an error if the plugin is not configured.
func (s *Server) getGitHubBroker(ctx context.Context) (*oauthcli.GitHubBroker, error) {
	state, err := s.pluginHost.Config().Get(ctx, pluginIDGitHub)
	if err != nil {
		return nil, fmt.Errorf("github plugin config unavailable: %w", err)
	}
	clientID, _ := state.Config["client_id"].(string)
	clientSecret, _ := state.Config["client_secret"].(string)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("github OAuth app is not configured (set client_id and client_secret in auth/github plugin)")
	}

	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.ghBroker == nil || s.ghBrokerClientID != clientID {
		s.ghBroker = oauthcli.NewGitHubBroker(oauthcli.GitHubConfig{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}, s.flowStore)
		s.ghBrokerClientID = clientID
	}
	return s.ghBroker, nil
}

// getLarkBroker returns the cached LarkBroker, lazily constructing it from the
// current plugin config. Returns an error if the plugin is not configured.
func (s *Server) getLarkBroker(ctx context.Context) (*oauthcli.LarkBroker, error) {
	state, err := s.pluginHost.Config().Get(ctx, pluginIDLark)
	if err != nil {
		return nil, fmt.Errorf("lark plugin config unavailable: %w", err)
	}
	appID, _ := state.Config["app_id"].(string)
	appSecret, _ := state.Config["app_secret"].(string)
	brand, _ := state.Config["brand"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("lark OAuth app is not configured (set app_id and app_secret in auth/lark plugin)")
	}
	if brand == "" {
		brand = "lark"
	}

	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.larkBroker == nil || s.larkBrokerAppID != appID {
		redirectURI := s.corsOriginV + larkCallbackPath
		s.larkBroker = oauthcli.NewLarkBroker(oauthcli.LarkConfig{
			AppID:     appID,
			AppSecret: appSecret,
			Brand:     brand,
		}, s.flowStore).WithRedirectURI(redirectURI)
		s.larkBrokerAppID = appID
	}
	return s.larkBroker, nil
}

// flowStatusJSON is the wire representation of an in-flight OAuth flow.
type flowStatusJSON struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flow_id"`
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresAt       string `json:"expires_at"`
	State           string `json:"state"`
}

func toFlowStatusJSON(fs oauthcli.FlowStatus) flowStatusJSON {
	return flowStatusJSON{
		Provider:        string(fs.Provider),
		FlowID:          fs.FlowID,
		VerificationURI: fs.VerificationURI,
		UserCode:        fs.UserCode,
		ExpiresAt:       fs.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		State:           string(fs.State),
	}
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
	ctx := r.Context()

	switch provider {
	case "github":
		broker, err := s.getGitHubBroker(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		status, err := broker.StartDeviceFlow(ctx, info.UserID)
		if err != nil {
			s.log.Error("start github device flow", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to start GitHub device flow")
			return
		}
		writeData(w, http.StatusOK, toFlowStatusJSON(status))

	case "lark":
		broker, err := s.getLarkBroker(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		status, err := broker.StartDeviceFlow(ctx, info.UserID)
		if err != nil {
			s.log.Error("start lark device flow", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to start Lark device flow")
			return
		}
		writeData(w, http.StatusOK, toFlowStatusJSON(status))

	default:
		writeError(w, http.StatusBadRequest, "unsupported provider: "+provider)
	}
}

// pollOAuthFlow handles GET /api/auth/profile/oauth/{provider}/status/{flowID}.
// For GitHub, if the flow is authorized this handler also calls Complete to
// persist the token bundle to vault.
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
	ctx := r.Context()

	switch provider {
	case "github":
		broker, err := s.getGitHubBroker(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		status, err := broker.Poll(ctx, flowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Persist token as soon as authorized.
		if status.State == oauthcli.FlowStateAuthorized {
			if cerr := broker.Complete(ctx, s.vaultSvc, info.UserID, flowID); cerr != nil {
				s.log.Error("complete github flow", "user_id", info.UserID, "flow_id", flowID, "error", cerr)
				writeError(w, http.StatusInternalServerError, "failed to save GitHub credentials")
				return
			}
		}
		writeData(w, http.StatusOK, toFlowStatusJSON(status))

	case "lark":
		broker, err := s.getLarkBroker(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		status, err := broker.Poll(ctx, flowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeData(w, http.StatusOK, toFlowStatusJSON(status))

	default:
		writeError(w, http.StatusBadRequest, "unsupported provider: "+provider)
	}
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
	ctx := r.Context()

	type connectedResp struct {
		Connected bool   `json:"connected"`
		Username  string `json:"username,omitempty"`
	}

	switch provider {
	case "github":
		bundle, err := oauthcli.LoadGHBundle(ctx, s.vaultSvc, info.UserID)
		if err != nil {
			s.log.Error("load gh bundle", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if bundle == nil {
			writeData(w, http.StatusOK, connectedResp{Connected: false})
			return
		}
		writeData(w, http.StatusOK, connectedResp{Connected: true})

	case "lark":
		bundle, err := oauthcli.LoadLarkBundle(ctx, s.vaultSvc, info.UserID)
		if err != nil {
			s.log.Error("load lark bundle", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if bundle == nil {
			writeData(w, http.StatusOK, connectedResp{Connected: false})
			return
		}
		label := bundle.AppID
		if bundle.Brand != "" {
			label = bundle.Brand + ":" + bundle.AppID
		}
		writeData(w, http.StatusOK, connectedResp{Connected: true, Username: label})

	default:
		writeError(w, http.StatusBadRequest, "unsupported provider: "+provider)
	}
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
	ctx := r.Context()

	var key string
	switch provider {
	case "github":
		key = oauthcli.VaultKeyGitHub
	case "lark":
		key = oauthcli.VaultKeyLark
	default:
		writeError(w, http.StatusBadRequest, "unsupported provider: "+provider)
		return
	}

	if err := oauthcli.DeleteBundle(ctx, s.vaultSvc, info.UserID, key); err != nil {
		s.log.Error("disconnect oauth", "provider", provider, "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to disconnect")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// larkOAuthCallback handles GET /api/auth/profile/oauth/lark/callback.
// Lark redirects the browser here after the user authorizes the app.
// Query params: code=<auth_code>&state=<flow_id>
func (s *Server) larkOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		http.Error(w, "vault not configured", http.StatusServiceUnavailable)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	flowID := r.URL.Query().Get("state")
	if code == "" || flowID == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	broker, err := s.getLarkBroker(ctx)
	if err != nil {
		http.Error(w, "lark not configured", http.StatusServiceUnavailable)
		return
	}

	if err := broker.Complete(ctx, s.vaultSvc, info.UserID, flowID, code); err != nil {
		s.log.Error("lark oauth complete", "user_id", info.UserID, "flow_id", flowID, "error", err)
		http.Error(w, "failed to complete Lark authorization: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/profile", http.StatusFound)
}
