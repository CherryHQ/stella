package main

import (
	"github.com/CherryHQ/stella/internal/platform/version"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/plugins/channels/weixin"
)

// weixinRegistrar adapts the weixin plugin's iLink client to the narrow
// server.WeixinRegistrar port. It lives in the composition root because it is the
// only place allowed to import the weixin plugin; the admin server depends on the
// port alone. A fresh client per call mirrors the previous handler behaviour
// (the QR/registration flows are unauthenticated, low-frequency admin actions).
type weixinRegistrar struct {
	baseURL string
}

// newWeixinRegistrar builds the registrar against the default iLink base URL.
func newWeixinRegistrar() server.WeixinRegistrar {
	return weixinRegistrar{baseURL: weixin.DefaultBaseURL}
}

func (r weixinRegistrar) GetQRCode() (server.WeixinQRCode, error) {
	qr, err := weixin.NewClient(r.baseURL, "", "", "", version.Version).GetQRCode()
	if err != nil {
		return server.WeixinQRCode{}, err
	}
	return server.WeixinQRCode{
		QRCode:           qr.QRCode,
		QRCodeImgContent: qr.QRCodeImgContent,
	}, nil
}

func (r weixinRegistrar) GetQRCodeStatus(qrcode string) (server.WeixinQRCodeStatus, error) {
	st, err := weixin.NewClient(r.baseURL, "", "", "", version.Version).GetQRCodeStatus(qrcode)
	if err != nil {
		return server.WeixinQRCodeStatus{}, err
	}
	return server.WeixinQRCodeStatus{
		Status:      st.Status,
		BotToken:    st.BotToken,
		ILinkBotID:  st.ILinkBotID,
		ILinkUserID: st.ILinkUserID,
		BaseURL:     st.BaseURL,
	}, nil
}
