package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/plugins/channels/weixin"
	"github.com/vaayne/anna/internal/config"
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	client := weixin.NewClient("", "", "")
	_ = ctx
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
			if _, err := s.authStore.GetIdentityByPlatform(r.Context(), channel.PlatformWeixin, externalID); err != nil {
				// Identity doesn't exist yet — create it.
				if _, err := s.authStore.CreateIdentity(r.Context(), auth.Identity{
					UserID:     info.UserID,
					Platform:   channel.PlatformWeixin,
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
// plugin config in the DB.
func (s *Server) saveWeixinCredentials(ctx context.Context, status *weixin.QRCodeStatusResponse) error {
	pluginID := config.PluginID(config.PluginKindChannel, channel.PlatformWeixin)
	p, err := s.store.GetPlugin(ctx, pluginID)
	if err != nil {
		p = config.Plugin{
			ID:      pluginID,
			Kind:    config.PluginKindChannel,
			Name:    channel.PlatformWeixin,
			Enabled: true,
			Config:  make(map[string]any),
		}
	}
	if p.Config == nil {
		p.Config = make(map[string]any)
	}

	p.Config["bot_token"] = status.BotToken
	p.Config["base_url"] = status.BaseURL
	p.Config["bot_id"] = status.ILinkBotID
	p.Config["user_id"] = status.ILinkUserID

	return s.store.UpsertPlugin(ctx, p)
}
