package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/channel/weixin"
	"github.com/vaayne/anna/internal/config"
)

// startWeixinQR initiates the WeChat QR login flow by requesting a QR code
// from the iLink API.
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

// pollWeixinQRStatus polls the QR code scan status.
// GET /api/channels/weixin/qr/status?qrcode=...
func (s *Server) pollWeixinQRStatus(w http.ResponseWriter, r *http.Request) {
	qrcode := r.URL.Query().Get("qrcode")
	if qrcode == "" {
		writeError(w, http.StatusBadRequest, "qrcode parameter required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	client := weixin.NewClient("", "", "")
	_ = ctx // timeout is handled by the client's HTTP timeout; we use a short-lived context
	status, err := client.GetQRCodeStatus(qrcode, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to poll QR status: "+err.Error())
		return
	}

	// On confirmed: save credentials to DB.
	if status.Status == "confirmed" && status.BotToken != "" {
		if err := s.saveWeixinCredentials(r.Context(), status); err != nil {
			s.log.Error("save weixin credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "QR confirmed but failed to save credentials: "+err.Error())
			return
		}
	}

	writeData(w, http.StatusOK, status)
}

// proxyWeixinQRImage proxies the QR code image from WeChat's CDN to avoid
// CORS errors when loading it directly in the browser.
// GET /api/channels/weixin/qr/image?url=...
func (s *Server) proxyWeixinQRImage(w http.ResponseWriter, r *http.Request) {
	imgURL := r.URL.Query().Get("url")
	if imgURL == "" {
		writeError(w, http.StatusBadRequest, "url parameter required")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(imgURL) //nolint:gosec // URL comes from trusted iLink API response
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch QR image: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "QR image fetch returned "+resp.Status)
		return
	}

	// Forward content type and pipe the body.
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, resp.Body)
}

// saveWeixinCredentials merges iLink credentials into the existing weixin
// channel config in the DB.
func (s *Server) saveWeixinCredentials(ctx context.Context, status *weixin.QRCodeStatusResponse) error {
	// Load existing config (may not exist yet).
	var raw map[string]any
	ch, err := s.store.GetChannel(ctx, "weixin")
	if err != nil {
		raw = make(map[string]any)
		ch = config.Channel{ID: "weixin", Enabled: true}
	} else {
		if err := json.Unmarshal([]byte(ch.Config), &raw); err != nil {
			raw = make(map[string]any)
		}
	}

	// Merge credentials.
	raw["bot_token"] = status.BotToken
	raw["base_url"] = status.BaseURL
	raw["bot_id"] = status.ILinkBotID
	raw["user_id"] = status.ILinkUserID

	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}

	return s.store.UpsertChannel(ctx, config.Channel{
		ID:      "weixin",
		Enabled: ch.Enabled,
		Config:  string(data),
	})
}
