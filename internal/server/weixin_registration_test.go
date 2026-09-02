package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/server"
)

func TestBeginWeixinRegistrationProxiesQRCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/get_bot_qrcode" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("bot_type"); got != "3" {
			t.Fatalf("bot_type = %q, want 3", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret":                0,
			"qrcode":             "qr-1",
			"qrcode_img_content": "https://wx.example/qr-1",
		})
	}))
	defer upstream.Close()
	defer server.SetWeixinRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/qr", map[string]any{})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		QRCode       string `json:"qrcode"`
		QRImageURL   string `json:"qr_image_url"`
		PollInterval int    `json:"poll_interval"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal begin: %v", err)
	}
	if got.QRCode != "qr-1" || got.QRImageURL != "https://wx.example/qr-1" || got.PollInterval != 2 {
		t.Fatalf("begin response = %#v", got)
	}
}

func TestPollWeixinRegistrationCreatesChannel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/get_qrcode_status" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("qrcode"); got != "qr-1" {
			t.Fatalf("qrcode = %q, want qr-1", got)
		}
		if got := r.Header.Get("iLink-App-ClientVersion"); got == "" {
			t.Fatalf("missing iLink-App-ClientVersion header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret":           0,
			"status":        "confirmed",
			"bot_token":     "wx-token",
			"ilink_bot_id":  "bot-1",
			"ilink_user_id": "user-1",
			"baseurl":       "https://wx.example",
		})
	}))
	defer upstream.Close()
	defer server.SetWeixinRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/poll", map[string]any{
		"qrcode":   "qr-1",
		"agent_id": agentID,
		"name":     "WeChat Auto",
		"config": map[string]any{
			"sk_route_tag": "stella-test",
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		Status  string `json:"status"`
		Channel struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			AgentID string `json:"agent_id"`
			Config  string `json:"config"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal poll: %v", err)
	}
	if got.Status != "created" || got.Channel.ID != "weixin" || got.Channel.Type != "weixin" || got.Channel.AgentID != agentID {
		t.Fatalf("poll response = %#v", got)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(got.Channel.Config), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg["bot_token"] != "wx-token" || cfg["base_url"] != "https://wx.example" || cfg["bot_id"] != "bot-1" || cfg["user_id"] != "user-1" || cfg["sk_route_tag"] != "stella-test" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestPollWeixinRegistrationDeniesNonAdminBeforeAgentLookup(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	for _, agent := range []config.Agent{
		{ID: "weixin-enabled", Name: "Enabled", Model: "test", Enabled: true},
		{ID: "weixin-disabled", Name: "Disabled", Model: "test", Enabled: false},
	} {
		if err := env.store.CreateAgent(ctx, agent); err != nil {
			t.Fatalf("CreateAgent(%s): %v", agent.ID, err)
		}
	}
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "weixin-regular", auth.RoleUser)
	for _, agentID := range []string{"missing-agent", "weixin-disabled", "weixin-enabled"} {
		rr := doRequestWithSession(t, env.srv, token, "POST", "/api/channels/weixin/register/poll", map[string]any{
			"qrcode": "qr", "agent_id": agentID,
		})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("agent %q: status = %d, want 403 (body: %s)", agentID, rr.Code, rr.Body.String())
		}
	}
}

func TestPollWeixinRegistrationRejectsCustomChannelID(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/poll", map[string]any{
		"qrcode":     "qr-1",
		"channel_id": "weixin-auto",
		"agent_id":   findStellaID(t, env),
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "weixin supports only the default channel id weixin" {
		t.Fatalf("error = %q", got)
	}
}

func TestPollWeixinRegistrationRequiresAgent(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/poll", map[string]any{
		"qrcode": "qr-1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "agent_id is required; bind this WeChat channel to an agent" {
		t.Fatalf("error = %q", got)
	}
}

func TestPollWeixinRegistrationRejectsSamePlatformAgentBinding(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	if err := env.store.UpsertChannel(context.Background(), config.Channel{
		ID:      "weixin-existing",
		Name:    "Existing WeChat",
		Type:    "weixin",
		AgentID: agentID,
		Enabled: true,
		Config:  `{"bot_token":"existing"}`,
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/poll", map[string]any{
		"qrcode":   "qr-1",
		"agent_id": agentID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "agent is already bound to weixin channel weixin-existing" {
		t.Fatalf("error = %q", got)
	}
}

func TestPollWeixinRegistrationPendingDoesNotCreateChannel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "status": "wait"})
	}))
	defer upstream.Close()
	defer server.SetWeixinRegistrationEndpointForTesting(upstream.URL)()

	env := setupAdmin(t)
	rr := doRequest(t, env, "POST", "/api/channels/weixin/register/poll", map[string]any{
		"qrcode":   "qr-1",
		"agent_id": findStellaID(t, env),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got struct {
		Status       string `json:"status"`
		PollInterval int    `json:"poll_interval"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if got.Status != "wait" || got.PollInterval != 2 {
		t.Fatalf("pending response = %#v", got)
	}
	if _, err := env.store.GetChannel(context.Background(), "weixin"); err == nil {
		t.Fatalf("channel was created while registration was pending")
	}
}
