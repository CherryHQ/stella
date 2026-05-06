package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/plugins/channels/weixin"
)

// startWeixinQR initiates the WeChat QR login flow by requesting a QR code
// from the iLink API. Any authenticated user can call this.
// POST /api/channels/weixin/qr
func (s *Server) startWeixinQR(w http.ResponseWriter, r *http.Request) {
	client := weixin.NewClient("", "", "")
	qr, err := client.GetQRCode("")
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to get QR code: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, qr)
}

// pollWeixinQRStatus polls the QR code scan status. On confirmed, saves
// channel credentials to DB and creates an auth identity linking the
// current user to the weixin account.
// GET /api/channels/weixin/qr/status?qrcode=...
func (s *Server) pollWeixinQRStatus(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	qrcode := r.URL.Query().Get("qrcode")
	if qrcode == "" {
		writeError(w, http.StatusBadRequest, "qrcode parameter required")
		return
	}

	client := weixin.NewClient("", "", "")
	status, err := client.GetQRCodeStatus(qrcode, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to poll QR status: "+err.Error())
		return
	}

	// On confirmed: save channel credentials and link user identity.
	if status.Status == "confirmed" && status.BotToken != "" {
		if err := s.saveWeixinCredentials(r.Context(), status); err != nil {
			s.log.Error("save weixin credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "QR confirmed but failed to save credentials: "+err.Error())
			return
		}

		// Link auth identity if not already linked.
		externalID := status.ILinkUserID
		if externalID == "" {
			externalID = status.ILinkBotID
		}
		if externalID != "" && s.authStore != nil {
			if _, err := s.authStore.GetIdentityByPlatform(r.Context(), pkgchannel.PlatformWeixin, externalID); err != nil {
				// Identity doesn't exist yet — create it.
				if _, err := s.authStore.CreateIdentity(r.Context(), auth.Identity{
					UserID:     info.UserID,
					Platform:   pkgchannel.PlatformWeixin,
					ExternalID: externalID,
				}); err != nil {
					s.log.Warn("create weixin identity", "user_id", info.UserID, "error", err)
				}
			}
		}
	}

	writeData(w, http.StatusOK, status)
}

// saveWeixinCredentials merges iLink credentials into the existing weixin
// channel instance config in the DB.
func (s *Server) saveWeixinCredentials(ctx context.Context, status *weixin.QRCodeStatusResponse) error {
	pluginID := config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin)
	ch, err := s.store.GetChannel(ctx, pkgchannel.PlatformWeixin)
	cfg := make(map[string]any)
	if err != nil {
		ch = config.Channel{
			ID:      pkgchannel.PlatformWeixin,
			Type:    pkgchannel.PlatformWeixin,
			Enabled: true,
		}
	} else if ch.Config != "" {
		_ = json.Unmarshal([]byte(ch.Config), &cfg)
		if cfg == nil {
			cfg = make(map[string]any)
		}
	}
	if ch.Type == "" {
		ch.Type = pkgchannel.PlatformWeixin
	}

	cfg["bot_token"] = status.BotToken
	cfg["base_url"] = status.BaseURL
	cfg["bot_id"] = status.ILinkBotID
	cfg["user_id"] = status.ILinkUserID

	plugin := config.Plugin{
		ID:      pluginID,
		Kind:    config.PluginKindChannel,
		Name:    pkgchannel.PlatformWeixin,
		Enabled: ch.Enabled,
		Config:  cfg,
	}
	if err := s.store.UpsertPlugin(ctx, plugin); err != nil {
		return err
	}

	if s.pluginHost != nil {
		if err := s.pluginHost.ValidateConfig(pluginID, cfg); err != nil {
			return err
		}
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	ch.Config = string(cfgJSON)
	if err := s.store.UpsertChannel(ctx, ch); err != nil {
		return err
	}
	if s.pluginHost != nil {
		if err := s.pluginHost.ApplyChannel(ctx, ch); err != nil {
			s.log.Error("failed to apply channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
		}
	}
	return nil
}
