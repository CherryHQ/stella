package server

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/auth"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// StartWeixinQR initiates the WeChat QR login flow by requesting a QR code
// from the iLink API. Any authenticated user can call this.
// POST /api/channels/weixin/qr
func (s *Server) StartWeixinQR(w http.ResponseWriter, r *http.Request) {
	qr, err := s.weixinRegistrar.GetQRCode()
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}
	writeData(w, http.StatusOK, qr)
}

// PollWeixinQRStatus polls the QR code scan status. On confirmed, it links the
// current user's identity to the weixin account. Provisioning the singleton
// channel credentials is an admin-only concern (see BeginWeixinRegistration /
// PollWeixinRegistration), so the credential write only happens for admins; a
// non-admin linking their identity must not overwrite the global channel.
// GET /api/channels/weixin/qr/status?qrcode=...
func (s *Server) PollWeixinQRStatus(w http.ResponseWriter, r *http.Request, params apiserver.PollWeixinQRStatusParams) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	qrcode := params.Qrcode
	if qrcode == "" {
		writeError(w, http.StatusBadRequest, "qrcode parameter required")
		return
	}

	status, err := s.weixinRegistrar.GetQRCodeStatus(qrcode)
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}

	// On confirmed: (admins only) provision channel credentials, then link the
	// current user's identity regardless of role.
	if status.Status == "confirmed" && status.BotToken != "" {
		if info.IsAdmin {
			if err := s.saveWeixinCredentials(r.Context(), status); err != nil {
				s.log.Error("save weixin credentials", "error", err)
				s.writeInternalError(w, err)
				return
			}
		}

		// Link channel identity if not already linked.
		externalID := status.ILinkUserID
		if externalID == "" {
			externalID = status.ILinkBotID
		}
		if externalID != "" && s.users != nil {
			if _, err := s.users.GetChannelIdentityByPlatform(r.Context(), pkgchannel.PlatformWeixin, externalID); err != nil {
				// Identity doesn't exist yet — create it.
				if _, err := s.users.CreateChannelIdentity(r.Context(), auth.ChannelIdentity{
					ID:         uuid.Must(uuid.NewV7()).String(),
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
func (s *Server) saveWeixinCredentials(ctx context.Context, status WeixinQRCodeStatus) error {
	_, err := s.saveWeixinSingletonChannel(ctx, "", "", false, nil, status)
	return err
}
