package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

const feishuRegistrationPath = "/oauth/v1/app/registration"

var (
	feishuRegistrationHTTPClient = &http.Client{Timeout: 20 * time.Second}
	feishuRegistrationAccounts   = map[string]string{
		"feishu": "https://accounts.feishu.cn",
		"lark":   "https://accounts.larksuite.com",
	}
)

func SetFeishuRegistrationEndpointForTesting(endpoint string) func() {
	previousClient := feishuRegistrationHTTPClient
	previousAccounts := feishuRegistrationAccounts
	feishuRegistrationHTTPClient = &http.Client{Timeout: 20 * time.Second}
	feishuRegistrationAccounts = map[string]string{"feishu": endpoint, "lark": endpoint}
	return func() {
		feishuRegistrationHTTPClient = previousClient
		feishuRegistrationAccounts = previousAccounts
	}
}

type feishuRegistrationBrandRequest struct {
	Brand string `json:"brand"`
}

type feishuRegistrationPollRequest struct {
	DeviceCode string         `json:"device_code"`
	ChannelID  string         `json:"channel_id"`
	AgentID    string         `json:"agent_id"`
	Name       string         `json:"name"`
	Brand      string         `json:"brand"`
	Enabled    *bool          `json:"enabled"`
	Config     map[string]any `json:"config"`
}

type feishuRegistrationUserInfo struct {
	OpenID      string `json:"open_id"`
	TenantBrand string `json:"tenant_brand"`
}

type feishuRegistrationUpstream struct {
	SupportedAuthMethods    []string                    `json:"supported_auth_methods"`
	DeviceCode              string                      `json:"device_code"`
	UserCode                string                      `json:"user_code"`
	VerificationURI         string                      `json:"verification_uri"`
	VerificationURIComplete string                      `json:"verification_uri_complete"`
	Interval                int                         `json:"interval"`
	ExpiresIn               int                         `json:"expires_in"`
	ClientID                string                      `json:"client_id"`
	ClientSecret            string                      `json:"client_secret"`
	UserInfo                *feishuRegistrationUserInfo `json:"user_info"`
	Error                   string                      `json:"error"`
	ErrorDescription        string                      `json:"error_description"`
}

func (s *Server) InitFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if _, err := access.ManageChannel("registration"); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	var req feishuRegistrationBrandRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	upstream, err := postFeishuRegistration(r, registrationBrand(req.Brand), url.Values{"action": {"init"}})
	if err != nil {
		writeFeishuRegistrationError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"supported_auth_methods": upstream.SupportedAuthMethods})
}

func (s *Server) BeginFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if _, err := access.ManageChannel("registration"); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	var req feishuRegistrationBrandRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	form := url.Values{
		"action":            {"begin"},
		"archetype":         {"PersonalAgent"},
		"auth_method":       {"client_secret"},
		"request_user_info": {"open_id tenant_brand"},
	}
	upstream, err := postFeishuRegistration(r, registrationBrand(req.Brand), form)
	if err != nil {
		writeFeishuRegistrationError(w, err)
		return
	}
	verificationURL := upstream.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = upstream.VerificationURI
	}
	qrURL := appendQueryParam(verificationURL, "from", "stella")
	writeData(w, http.StatusOK, map[string]any{
		"device_code":               upstream.DeviceCode,
		"user_code":                 upstream.UserCode,
		"verification_uri":          upstream.VerificationURI,
		"verification_uri_complete": verificationURL,
		"qr_url":                    qrURL,
		"interval":                  defaultInt(upstream.Interval, 5),
		"expires_in":                defaultInt(upstream.ExpiresIn, 300),
	})
}

func (s *Server) PollFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req feishuRegistrationPollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.DeviceCode = strings.TrimSpace(req.DeviceCode)
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, "device_code is required")
		return
	}
	// The client no longer invents channel ids, so an omitted channel_id is the
	// normal case: mint one here exactly as CreateChannel does. Only the poll that
	// returns credentials writes a row, so a fresh id per pending poll is inert.
	if req.ChannelID == "" {
		req.ChannelID = generateChannelID(pkgchannel.PlatformFeishu)
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required; bind this Feishu channel to an agent")
		return
	}
	prospective := config.Channel{ID: req.ChannelID, Type: pkgchannel.PlatformFeishu, AgentID: req.AgentID}
	operation, err := access.ManageChannel(req.ChannelID)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	agent, err := access.LookupAgent(r.Context(), req.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "agent_id must reference an existing agent")
		return
	}
	if !agent.Enabled {
		writeError(w, http.StatusBadRequest, "agent_id must reference an enabled agent")
		return
	}
	if err := operation.ValidateBinding(r.Context(), prospective); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	brand := registrationBrand(req.Brand)
	upstream, err := pollFeishuRegistrationUpstream(r, brand, req.DeviceCode)
	if err != nil {
		var pending feishuRegistrationPendingError
		if errors.As(err, &pending) {
			writeData(w, http.StatusOK, map[string]any{"status": pending.Status, "interval": pending.Interval})
			return
		}
		writeFeishuRegistrationError(w, err)
		return
	}
	if upstream.ClientSecret == "" && upstream.UserInfo != nil && upstream.UserInfo.TenantBrand == "lark" && brand != "lark" {
		upstream, err = pollFeishuRegistrationUpstream(r, "lark", req.DeviceCode)
		if err != nil {
			writeFeishuRegistrationError(w, err)
			return
		}
	}
	if upstream.ClientID == "" || upstream.ClientSecret == "" {
		writeError(w, http.StatusBadGateway, "Feishu registration did not return app credentials")
		return
	}

	cfg := req.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["app_id"] = upstream.ClientID
	cfg["app_secret"] = upstream.ClientSecret
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Feishu"
	}
	ch := config.Channel{ID: req.ChannelID, Name: name, Type: pkgchannel.PlatformFeishu, AgentID: req.AgentID, Enabled: enabled}
	saved, err := operation.Save(r.Context(), ch, cfg, true)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"status":       "created",
		"tenant_brand": tenantBrand(upstream),
		"channel":      channelToView(saved),
	})
}

func tenantBrand(upstream feishuRegistrationUpstream) string {
	if upstream.UserInfo == nil {
		return ""
	}
	return upstream.UserInfo.TenantBrand
}

func pollFeishuRegistrationUpstream(r *http.Request, brand, deviceCode string) (feishuRegistrationUpstream, error) {
	return postFeishuRegistration(r, brand, url.Values{"action": {"poll"}, "device_code": {deviceCode}})
}

type feishuRegistrationPendingError struct {
	Status   string
	Interval int
}

func (e feishuRegistrationPendingError) Error() string { return e.Status }

type feishuRegistrationUpstreamError struct {
	Message string
}

func (e feishuRegistrationUpstreamError) Error() string { return e.Message }

func postFeishuRegistration(r *http.Request, brand string, form url.Values) (feishuRegistrationUpstream, error) {
	endpoint := feishuRegistrationAccounts[brand] + feishuRegistrationPath
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return feishuRegistrationUpstream{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := feishuRegistrationHTTPClient.Do(httpReq)
	if err != nil {
		return feishuRegistrationUpstream{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return feishuRegistrationUpstream{}, err
	}
	var data feishuRegistrationUpstream
	if err := json.Unmarshal(body, &data); err != nil {
		return feishuRegistrationUpstream{}, fmt.Errorf("decode Feishu registration response: %w", err)
	}
	if data.Error != "" {
		switch data.Error {
		case "authorization_pending":
			return feishuRegistrationUpstream{}, feishuRegistrationPendingError{Status: "pending", Interval: defaultInt(data.Interval, 5)}
		case "slow_down":
			return feishuRegistrationUpstream{}, feishuRegistrationPendingError{Status: "slow_down", Interval: defaultInt(data.Interval, 10)}
		}
		message := data.ErrorDescription
		if message == "" {
			message = data.Error
		}
		return feishuRegistrationUpstream{}, feishuRegistrationUpstreamError{Message: message}
	}
	if resp.StatusCode >= 400 {
		return feishuRegistrationUpstream{}, feishuRegistrationUpstreamError{Message: fmt.Sprintf("Feishu registration API returned HTTP %d", resp.StatusCode)}
	}
	return data, nil
}

func writeFeishuRegistrationError(w http.ResponseWriter, err error) {
	var upstreamErr feishuRegistrationUpstreamError
	if errors.As(err, &upstreamErr) {
		writeError(w, http.StatusBadGateway, upstreamErr.Message)
		return
	}
	writeError(w, http.StatusBadGateway, "upstream service error")
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return json.Unmarshal(body, dst)
}

func registrationBrand(brand string) string {
	if brand == "lark" {
		return "lark"
	}
	return "feishu"
}

func appendQueryParam(rawURL, key, value string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
