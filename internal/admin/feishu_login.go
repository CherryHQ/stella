package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkauthen "github.com/larksuite/oapi-sdk-go/v3/service/authen/v1"
	"github.com/vaayne/anna/internal/auth"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// FeishuLoginAvailability describes the current state of Feishu login.
type FeishuLoginAvailability struct {
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
	HasConflict bool   `json:"has_conflict"`
}

// LoginEnabledFeishuConfig holds the resolved configuration for Feishu web login.
type LoginEnabledFeishuConfig struct {
	InstanceID    string
	AppID         string
	AppSecret     string
	TenantKey     string
	AutoProvision bool
}

// findLoginEnabledFeishuInstance discovers exactly one Feishu channel instance
// enabled for web login. It returns availability status and the config if found.
// The following states are possible:
//   - Available: exactly one valid login-enabled instance found
//   - Zero instances: not available, reason "no_login_instance"
//   - Multiple instances: not available, has_conflict=true, reason "multiple_login_instances"
//   - Missing credentials: not available, reason "missing_credentials"
func (s *Server) findLoginEnabledFeishuInstance(ctx context.Context) (FeishuLoginAvailability, *LoginEnabledFeishuConfig) {
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return FeishuLoginAvailability{
			Available: false,
			Reason:    "store_error",
		}, nil
	}

	var found *LoginEnabledFeishuConfig
	var foundCount int

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if ch.Type != pkgchannel.PlatformFeishu && ch.ID != pkgchannel.PlatformFeishu {
			continue
		}

		cfg, err := parseFeishuChannelConfig(ch.Config)
		if err != nil {
			continue
		}

		if !cfg.EnableLogin {
			continue
		}

		foundCount++
		if foundCount == 1 {
			found = &LoginEnabledFeishuConfig{
				InstanceID:    ch.ID,
				AppID:         cfg.AppID,
				AppSecret:     cfg.AppSecret,
				TenantKey:     cfg.TenantKey,
				AutoProvision: cfg.AutoProvision,
			}
		}
	}

	if foundCount == 0 {
		return FeishuLoginAvailability{
			Available: false,
			Reason:    "no_login_instance",
		}, nil
	}

	if foundCount > 1 {
		return FeishuLoginAvailability{
			Available:   false,
			HasConflict: true,
			Reason:      "multiple_login_instances",
		}, nil
	}

	// Validate credentials
	if found.AppID == "" || found.AppSecret == "" {
		return FeishuLoginAvailability{
			Available:  false,
			Reason:     "missing_credentials",
			InstanceID: found.InstanceID,
		}, nil
	}

	return FeishuLoginAvailability{
		Available:  true,
		InstanceID: found.InstanceID,
	}, found
}

// parseFeishuChannelConfig parses the JSON config string from a Feishu channel.
func parseFeishuChannelConfig(configJSON string) (*pkgchannel.FeishuConfig, error) {
	var cfg pkgchannel.FeishuConfig
	if configJSON == "" {
		return &cfg, nil
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("parse feishu config: %w", err)
	}
	return &cfg, nil
}

// feishuLoginAvailabilityHandler handles GET /api/auth/login/feishu/availability.
// This endpoint is public (unauthenticated) so the login page can show/hide the button.
func (s *Server) feishuLoginAvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	availability, _ := s.findLoginEnabledFeishuInstance(r.Context())
	writeData(w, http.StatusOK, availability)
}

// ============================================
// Phase 2: Login OAuth Flow State Management
// ============================================

// LoginFlowState holds one-time security-sensitive state for Feishu login.
// This state is consumed (deleted) on first read to prevent replay attacks.
type LoginFlowState struct {
	FlowID      string    // High-entropy random ID (state parameter in OAuth)
	Provider    string    // "feishu" (future-proofing for other providers)
	ChannelID   string    // The selected Feishu instance ID
	RedirectURL string    // Where to redirect after login (must be local/relative)
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Used        bool      // Set to true when consumed (defense in depth)
}

// LoginFlowStore manages in-flight login OAuth flows with consume-on-read semantics.
// This is separate from the profile OAuth FlowStore because login has stricter
// security requirements: one-time use, explicit expiry, and no reuse.
type LoginFlowStore struct {
	mu    sync.Mutex
	flows map[string]LoginFlowState
}

// NewLoginFlowStore creates a new login flow store.
func NewLoginFlowStore() *LoginFlowStore {
	return &LoginFlowStore{flows: make(map[string]LoginFlowState)}
}

// Create stores a new login flow state and returns the flow ID.
func (s *LoginFlowStore) Create(provider, channelID, redirectURL string, ttl time.Duration) string {
	flowID := generateFlowID()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.flows[flowID] = LoginFlowState{
		FlowID:      flowID,
		Provider:    provider,
		ChannelID:   channelID,
		RedirectURL: redirectURL,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
		Used:        false,
	}

	return flowID
}

// Consume retrieves a flow state by ID and deletes it from the store.
// Returns (state, true) if found and not expired, (_, false) otherwise.
// This prevents replay attacks by ensuring each flow ID can only be used once.
func (s *LoginFlowStore) Consume(flowID string) (LoginFlowState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.flows[flowID]
	if !ok {
		return LoginFlowState{}, false
	}

	// Always delete on first read (consume semantics)
	delete(s.flows, flowID)

	// Reject if already used (defense in depth)
	if state.Used {
		return LoginFlowState{}, false
	}

	// Reject if expired
	if time.Now().After(state.ExpiresAt) {
		return LoginFlowState{}, false
	}

	state.Used = true
	return state, true
}

// generateFlowID creates a high-entropy random flow ID.
func generateFlowID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return fmt.Sprintf("flow_%d_%d", time.Now().UnixNano(), time.Now().Nanosecond())
	}
	return hex.EncodeToString(b)
}

// isValidRedirectURL checks if a redirect URL is safe (local/relative only).
// This prevents open redirect vulnerabilities.
func isValidRedirectURL(redirect string) bool {
	if redirect == "" {
		return true // Empty redirect is valid (defaults to "/")
	}

	// Must start with "/" (relative URL)
	if !strings.HasPrefix(redirect, "/") {
		return false
	}

	// Must not have "//" which could indicate a protocol-relative URL
	if strings.HasPrefix(redirect, "//") {
		return false
	}

	// Parse and verify no host is set (prevents absolute URLs)
	u, err := url.Parse(redirect)
	if err != nil {
		return false
	}

	// Must not have a scheme or host (must be relative)
	if u.Scheme != "" || u.Host != "" {
		return false
	}

	return true
}

// feishuLoginFlowTTL is the time-to-live for login OAuth flow state.
const feishuLoginFlowTTL = 10 * time.Minute

// feishuLoginStartHandler handles POST /api/auth/login/feishu/start.
// Initiates the Feishu OAuth flow by creating state and returning the auth URL.
func (s *Server) feishuLoginStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get and validate the login-enabled Feishu instance
	availability, loginCfg := s.findLoginEnabledFeishuInstance(r.Context())
	if !availability.Available {
		switch availability.Reason {
		case "no_login_instance":
			writeError(w, http.StatusServiceUnavailable, "Feishu login is not configured")
		case "multiple_login_instances":
			writeError(w, http.StatusConflict, "Multiple Feishu login instances configured. Please configure exactly one.")
		case "missing_credentials":
			writeError(w, http.StatusServiceUnavailable, "Feishu login credentials incomplete")
		default:
			writeError(w, http.StatusServiceUnavailable, "Feishu login unavailable")
		}
		return
	}

	// Parse request body for optional redirect URL
	var body struct {
		RedirectURL string `json:"redirect_url,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validate redirect URL (prevent open redirect)
	if !isValidRedirectURL(body.RedirectURL) {
		writeError(w, http.StatusBadRequest, "invalid redirect URL")
		return
	}

	// Initialize login flow store if not already done
	if s.loginFlowStore == nil {
		s.loginFlowStore = NewLoginFlowStore()
	}

	// Create one-time state
	flowID := s.loginFlowStore.Create("feishu", loginCfg.InstanceID, body.RedirectURL, feishuLoginFlowTTL)

	// Build Feishu OAuth authorization URL
	// Note: Feishu uses app_id (not client_id) and requires state
	authURL := buildFeishuAuthURL(loginCfg.AppID, flowID)

	writeData(w, http.StatusOK, map[string]string{
		"auth_url": authURL,
		"state":    flowID,
	})
}

// buildFeishuAuthURL constructs the Feishu OAuth authorization URL.
// The redirect_uri is fixed to /api/auth/login/feishu/callback on the same host.
func buildFeishuAuthURL(appID, state string) string {
	// Feishu OAuth endpoint
	baseURL := "https://open.feishu.cn/open-apis/authen/v1/authorize"

	params := url.Values{}
	params.Set("app_id", appID)
	params.Set("redirect_uri", "/api/auth/login/feishu/callback")
	params.Set("state", state)
	// Request minimal scope for login (user info only, not offline_access)
	params.Set("scope", "")

	return baseURL + "?" + params.Encode()
}

// feishuLoginCallbackHandler handles GET /api/auth/login/feishu/callback.
// Completes the OAuth flow, resolves/provisions the user, and creates a session.
func (s *Server) feishuLoginCallbackHandler(w http.ResponseWriter, r *http.Request) {
	// Extract OAuth callback parameters
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "missing authorization code or state")
		return
	}

	// Initialize login flow store if not already done
	if s.loginFlowStore == nil {
		s.loginFlowStore = NewLoginFlowStore()
	}

	// Consume the one-time state (prevents replay)
	flowState, ok := s.loginFlowStore.Consume(state)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	// Verify provider matches
	if flowState.Provider != "feishu" {
		writeError(w, http.StatusBadRequest, "provider mismatch")
		return
	}

	// Get the login-enabled Feishu instance (must match the channel from state)
	availability, loginCfg := s.findLoginEnabledFeishuInstance(r.Context())
	if !availability.Available || loginCfg.InstanceID != flowState.ChannelID {
		writeError(w, http.StatusBadRequest, "channel configuration mismatch")
		return
	}

	// Exchange code for access token and fetch user info
	feishuUser, err := s.exchangeFeishuCodeAndGetUser(r.Context(), loginCfg, code)
	if err != nil {
		s.log.Error("feishu login: failed to exchange code or get user", "error", err)
		writeError(w, http.StatusUnauthorized, "failed to authenticate with Feishu")
		return
	}

	// Validate required fields
	if feishuUser.UnionID == "" {
		writeError(w, http.StatusUnauthorized, "Feishu user info incomplete: missing union_id")
		return
	}

	// TODO: Phase 3 - Resolve existing user by identity
	// TODO: Phase 4 - Canonicalize identity (migrate open_id to union_id)
	// TODO: Phase 5 - Provision new user if needed (with tenant/bootstrap checks)
	// TODO: Phase 5 - Create session and set cookie

	// For now, just return the user info (Phase 2 skeleton)
	writeData(w, http.StatusOK, map[string]any{
		"message":     "Feishu login callback received (Phase 2 - implementation pending)",
		"union_id":    feishuUser.UnionID,
		"open_id":     feishuUser.OpenID,
		"name":        feishuUser.Name,
		"email":       feishuUser.Email,
		"tenant_key":  feishuUser.TenantKey,
		"redirect_to": flowState.RedirectURL,
	})
}

// feishuUserInfo holds the essential user information from Feishu OAuth.
type feishuUserInfo struct {
	UnionID string
	OpenID  string
	Name    string
	Email   string
	TenantKey string
}

// exchangeFeishuCodeAndGetUser exchanges the OAuth code for an access token.
// Feishu returns the user info directly in the token exchange response.
func (s *Server) exchangeFeishuCodeAndGetUser(ctx context.Context, cfg *LoginEnabledFeishuConfig, code string) (*feishuUserInfo, error) {
	// Create Feishu client
	client := lark.NewClient(cfg.AppID, cfg.AppSecret)

	// Exchange code for access token
	tokenReq := larkauthen.NewCreateAccessTokenReqBuilder().
		Body(larkauthen.NewCreateAccessTokenReqBodyBuilder().
			GrantType("authorization_code").
			Code(code).
			Build()).
		Build()

	tokenResp, err := client.Authen.AccessToken.Create(ctx, tokenReq)
	if err != nil {
		return nil, fmt.Errorf("exchange code for token: %w", err)
	}

	if !tokenResp.Success() {
		return nil, fmt.Errorf("token exchange failed: code=%d, msg=%s", tokenResp.Code, tokenResp.Msg)
	}

	if tokenResp.Data == nil {
		return nil, fmt.Errorf("token response empty")
	}

	// Feishu returns user info directly in the token response
	data := tokenResp.Data
	info := &feishuUserInfo{}

	if data.AccessToken != nil {
		// Access token available but not needed for our flow
		_ = *data.AccessToken
	}
	if data.UnionId != nil {
		info.UnionID = *data.UnionId
	}
	if data.OpenId != nil {
		info.OpenID = *data.OpenId
	}
	if data.Name != nil {
		info.Name = *data.Name
	}
	if data.Email != nil {
		info.Email = *data.Email
	}
	if data.TenantKey != nil {
		info.TenantKey = *data.TenantKey
	}

	return info, nil
}

// createSessionAndSetCookie creates an auth session and sets the session cookie.
// This is extracted from auth.go to be reused by both password and Feishu login.
func (s *Server) createSessionAndSetCookie(w http.ResponseWriter, r *http.Request, userID int64) error {
	ctx := r.Context()

	sessionID := auth.NewSessionID()
	_, err := s.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    userID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	secure := !isLocalhost(r)
	auth.SetSessionCookie(w, sessionID, secure)

	return nil
}
