package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/server"
)

func TestBeginFeishuRegistrationProxiesDeviceFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/app/registration" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		assertForm(t, r.Form, "action", "begin")
		assertForm(t, r.Form, "archetype", "PersonalAgent")
		assertForm(t, r.Form, "auth_method", "client_secret")
		assertForm(t, r.Form, "request_user_info", "open_id tenant_brand")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-1",
			"user_code":                 "user-1",
			"verification_uri":          "https://open.feishu.cn/page/cli",
			"verification_uri_complete": "https://open.feishu.cn/page/cli?user_code=user-1",
			"interval":                  2,
			"expires_in":                300,
		})
	}))
	defer upstream.Close()
	defer server.SetFeishuRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/feishu/register/begin", map[string]any{})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		DeviceCode string `json:"device_code"`
		QrURL      string `json:"qr_url"`
		Interval   int    `json:"interval"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal begin: %v", err)
	}
	if got.DeviceCode != "device-1" || got.Interval != 2 {
		t.Fatalf("begin response = %#v", got)
	}
	qr, err := url.Parse(got.QrURL)
	if err != nil {
		t.Fatalf("parse qr_url: %v", err)
	}
	if qr.Query().Get("from") != "stella" || qr.Query().Get("user_code") != "user-1" {
		t.Fatalf("qr_url query = %v", qr.Query())
	}
}

func TestPollFeishuRegistrationCreatesChannel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		assertForm(t, r.Form, "action", "poll")
		assertForm(t, r.Form, "device_code", "device-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "cli_a",
			"client_secret": "sec_b",
			"user_info": map[string]any{
				"open_id":      "ou_x",
				"tenant_brand": "feishu",
			},
		})
	}))
	defer upstream.Close()
	defer server.SetFeishuRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	rr := doRequest(t, env, "POST", "/api/channels/feishu/register/poll", map[string]any{
		"device_code": "device-1",
		"channel_id":  "feishu-auto",
		"agent_id":    agentID,
		"name":        "Feishu Auto",
		"config": map[string]any{
			"auto_provision": true,
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		Status      string `json:"status"`
		TenantBrand string `json:"tenant_brand"`
		Channel     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			AgentID string `json:"agent_id"`
			Config  string `json:"config"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal poll: %v", err)
	}
	if got.Status != "created" || got.TenantBrand != "feishu" || got.Channel.ID != "feishu-auto" || got.Channel.Type != "feishu" || got.Channel.AgentID != agentID {
		t.Fatalf("poll response = %#v", got)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(got.Channel.Config), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg["app_id"] != "cli_a" || cfg["app_secret"] != "sec_b" || cfg["auto_provision"] != true {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestPollFeishuRegistrationRequiresAgent(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/feishu/register/poll", map[string]any{
		"device_code": "device-1",
		"channel_id":  "feishu-auto",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "agent_id is required; bind this Feishu channel to an agent" {
		t.Fatalf("error = %q", got)
	}
}

func TestPollFeishuRegistrationDeniesNonAdminBeforeAgentLookup(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	for _, agent := range []config.Agent{
		{ID: "feishu-enabled", Name: "Enabled", Model: "test", Enabled: true},
		{ID: "feishu-disabled", Name: "Disabled", Model: "test", Enabled: false},
	} {
		if err := env.store.CreateAgent(ctx, agent); err != nil {
			t.Fatalf("CreateAgent(%s): %v", agent.ID, err)
		}
	}
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "feishu-regular", auth.RoleUser)
	for _, agentID := range []string{"missing-agent", "feishu-disabled", "feishu-enabled"} {
		rr := doRequestWithSession(t, env.srv, token, "POST", "/api/channels/feishu/register/poll", map[string]any{
			"device_code": "code", "channel_id": "feishu-test", "agent_id": agentID,
		})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("agent %q: status = %d, want 403 (body: %s)", agentID, rr.Code, rr.Body.String())
		}
	}
}

func TestPollFeishuRegistrationRejectsSamePlatformAgentBinding(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	if err := env.store.CreateChannel(context.Background(), config.Channel{
		ID:      "feishu-existing",
		Name:    "Existing Feishu",
		Type:    "feishu",
		AgentID: agentID,
		Enabled: true,
		Config:  `{"app_id":"existing","app_secret":"secret"}`,
	}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	rr := doRequest(t, env, "POST", "/api/channels/feishu/register/poll", map[string]any{
		"device_code": "device-1",
		"channel_id":  "feishu-auto",
		"agent_id":    agentID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "agent is already bound to feishu channel feishu-existing" {
		t.Fatalf("error = %q", got)
	}
}

func TestPollFeishuRegistrationPendingDoesNotCreateChannel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending", "interval": 4})
	}))
	defer upstream.Close()
	defer server.SetFeishuRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/feishu/register/poll", map[string]any{
		"device_code": "device-1",
		"channel_id":  "feishu-auto",
		"agent_id":    findStellaID(t, env),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		Status   string `json:"status"`
		Interval int    `json:"interval"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if got.Status != "pending" || got.Interval != 4 {
		t.Fatalf("pending response = %#v", got)
	}
}

func assertForm(t *testing.T, form url.Values, key, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Fatalf("form[%s] = %q, want %q", key, got, want)
	}
}
