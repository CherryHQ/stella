package server

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/plugins/channels/weixin"
)

const weixinRegistrationPollInterval = 2

var weixinRegistrationEndpoint = weixin.DefaultBaseURL

func SetWeixinRegistrationEndpointForTesting(endpoint string) func() {
	previous := weixinRegistrationEndpoint
	weixinRegistrationEndpoint = endpoint
	return func() { weixinRegistrationEndpoint = previous }
}

type weixinRegistrationPollRequest struct {
	QRCode    string         `json:"qrcode"`
	ChannelID string         `json:"channel_id"`
	AgentID   string         `json:"agent_id"`
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
}

func getWeixinRegistrationQRCode() (*weixin.QRCodeResponse, error) {
	return weixin.NewClient(weixinRegistrationEndpoint, "", "", "").GetQRCode()
}

func getWeixinRegistrationStatus(qrcode string) (*weixin.QRCodeStatusResponse, error) {
	return weixin.NewClient(weixinRegistrationEndpoint, "", "", "").GetQRCodeStatus(qrcode)
}

func (s *Server) BeginWeixinRegistration(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	qr, err := getWeixinRegistrationQRCode()
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"qrcode":        qr.QRCode,
		"qr_image_url":  qr.QRCodeImgContent,
		"poll_interval": weixinRegistrationPollInterval,
	})
}

func (s *Server) PollWeixinRegistration(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var req weixinRegistrationPollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.QRCode = strings.TrimSpace(req.QRCode)
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.ChannelID == "" {
		req.ChannelID = pkgchannel.PlatformWeixin
	}
	if req.QRCode == "" {
		writeError(w, http.StatusBadRequest, "qrcode is required")
		return
	}
	if req.ChannelID != pkgchannel.PlatformWeixin {
		writeError(w, http.StatusBadRequest, "weixin supports only the default channel id weixin")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required; bind this WeChat channel to an agent")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), req.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "agent_id must reference an existing agent")
		return
	}
	if !agent.Enabled {
		writeError(w, http.StatusBadRequest, "agent_id must reference an enabled agent")
		return
	}
	ch := config.Channel{ID: pkgchannel.PlatformWeixin, Type: pkgchannel.PlatformWeixin, AgentID: req.AgentID}
	if conflict, err := s.channelAgentPlatformBindingConflict(r.Context(), ch); err != nil {
		s.writeInternalError(w, err)
		return
	} else if conflict != "" {
		writeError(w, http.StatusBadRequest, conflict)
		return
	}

	status, err := getWeixinRegistrationStatus(req.QRCode)
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}
	if status.Status != "confirmed" {
		writeData(w, http.StatusOK, map[string]any{"status": status.Status, "poll_interval": weixinRegistrationPollInterval})
		return
	}
	if status.BotToken == "" {
		writeError(w, http.StatusBadGateway, "WeChat registration did not return bot credentials")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "WeChat"
	}
	ch.Name = name
	ch.Enabled = true
	saved, err := s.saveWeixinSingletonChannel(r.Context(), ch, req.Config, status)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"status": "created", "channel": channelToView(saved)})
}

func (s *Server) saveWeixinSingletonChannel(ctx context.Context, patch config.Channel, cfgPatch map[string]any, status *weixin.QRCodeStatusResponse) (config.Channel, error) {
	ch, err := s.store.GetChannel(ctx, pkgchannel.PlatformWeixin)
	cfg := map[string]any{}
	if err != nil {
		ch = config.Channel{ID: pkgchannel.PlatformWeixin, Type: pkgchannel.PlatformWeixin, Enabled: true}
	} else if ch.Config != "" {
		_ = json.Unmarshal([]byte(ch.Config), &cfg)
		if cfg == nil {
			cfg = map[string]any{}
		}
	}
	if ch.Type == "" {
		ch.Type = pkgchannel.PlatformWeixin
	}
	if patch.Name != "" {
		ch.Name = patch.Name
	}
	if patch.AgentID != "" {
		ch.AgentID = patch.AgentID
	}
	// Enabled is one-way on purpose: a confirmed registration enables the
	// channel, while the QR-login path passes a zero patch to leave the
	// existing enabled state untouched. There is no caller that disables here.
	if patch.Enabled {
		ch.Enabled = true
	}
	maps.Copy(cfg, cfgPatch)
	cfg["bot_token"] = status.BotToken
	cfg["base_url"] = status.BaseURL
	cfg["bot_id"] = status.ILinkBotID
	cfg["user_id"] = status.ILinkUserID

	pluginID := config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin)
	if s.pluginHost != nil {
		if err := s.pluginHost.ValidateConfig(pluginID, cfg); err != nil {
			return config.Channel{}, err
		}
	}
	if err := s.store.UpsertPlugin(ctx, config.Plugin{ID: pluginID, Kind: config.PluginKindChannel, Name: pkgchannel.PlatformWeixin, Enabled: ch.Enabled, Config: cfg}); err != nil {
		return config.Channel{}, err
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return config.Channel{}, err
	}
	ch.ID = pkgchannel.PlatformWeixin
	ch.Type = pkgchannel.PlatformWeixin
	ch.Config = string(cfgJSON)
	if err := s.store.UpsertChannel(ctx, ch); err != nil {
		return config.Channel{}, err
	}
	if s.pluginHost != nil {
		if err := s.pluginHost.ApplyChannel(ctx, ch); err != nil {
			s.log.Error("failed to apply channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
		}
	}
	return s.store.GetChannel(ctx, ch.ID)
}
