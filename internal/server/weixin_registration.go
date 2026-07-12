package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

const weixinRegistrationPollInterval = 2

// errWeixinConfigInvalid wraps plugin config-validation failures so callers can
// distinguish a bad caller-supplied config (HTTP 400) from a store/runtime
// failure (HTTP 500).
var errWeixinConfigInvalid = errors.New("invalid weixin channel config")

// validateWeixinChannelID enforces the WeChat singleton invariant: one iLink
// account cannot back multiple independent bots, so the only valid weixin
// channel ID is the canonical "weixin".
func validateWeixinChannelID(id string) error {
	if id != pkgchannel.PlatformWeixin {
		return errors.New("weixin supports only the default channel id weixin")
	}
	return nil
}

type weixinRegistrationPollRequest struct {
	QRCode    string         `json:"qrcode"`
	ChannelID string         `json:"channel_id"`
	AgentID   string         `json:"agent_id"`
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
}

func (s *Server) BeginWeixinRegistration(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.AuthorizeChannelRegistration(config.Channel{ID: pkgchannel.PlatformWeixin, Type: pkgchannel.PlatformWeixin}); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	qr, err := s.weixinRegistrar.GetQRCode()
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
	access, ok := s.beginControlPlane(w, r)
	if !ok {
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
	if err := validateWeixinChannelID(req.ChannelID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	if err := access.AuthorizeChannelRegistration(ch); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	if conflict, err := s.channelAgentPlatformBindingConflict(r.Context(), ch); err != nil {
		s.writeInternalError(w, err)
		return
	} else if conflict != "" {
		writeError(w, http.StatusBadRequest, conflict)
		return
	}

	status, err := s.weixinRegistrar.GetQRCodeStatus(req.QRCode)
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
	saved, err := s.saveWeixinSingletonChannel(r.Context(), name, req.AgentID, true, req.Config, status)
	if errors.Is(err, errWeixinConfigInvalid) {
		writeError(w, http.StatusBadRequest, "invalid channel config")
		return
	}
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"status": "created", "channel": channelToView(saved)})
}

// saveWeixinSingletonChannel upserts the canonical "weixin" channel with the
// iLink credentials from status, merging cfgPatch first so the credentials
// always win. name and agentID are applied only when non-empty; enable only
// ever turns the channel on (a confirmed registration enables it; the
// identity-link path passes false to leave the existing state untouched).
func (s *Server) saveWeixinSingletonChannel(ctx context.Context, name, agentID string, enable bool, cfgPatch map[string]any, status WeixinQRCodeStatus) (config.Channel, error) {
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
	if name != "" {
		ch.Name = name
	}
	if agentID != "" {
		ch.AgentID = agentID
	}
	if enable {
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
			return config.Channel{}, fmt.Errorf("%w: %w", errWeixinConfigInvalid, err)
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
