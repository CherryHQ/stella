package server

import (
	weixin "github.com/CherryHQ/stella/plugins/channels/weixin"
)

// weixinTestEndpoint is the iLink base URL the test registrar dials. Tests point
// it at an httptest server via SetWeixinRegistrationEndpointForTesting. It is
// read at each call so an override takes effect for the request under test,
// mirroring the production adapter's per-call client construction.
var weixinTestEndpoint = weixin.DefaultBaseURL

// SetWeixinRegistrationEndpointForTesting overrides the iLink endpoint the test
// WeChat registrar dials and returns a restore func. It lives in a _test.go file
// so the production build never imports the weixin plugin; both the internal
// (package server) and external (package server_test) test files share it.
func SetWeixinRegistrationEndpointForTesting(endpoint string) func() {
	previous := weixinTestEndpoint
	weixinTestEndpoint = endpoint
	return func() { weixinTestEndpoint = previous }
}

// testWeixinRegistrar wraps the weixin plugin client for tests, mapping its
// responses to the server-local port result types exactly as the composition
// root's adapter does.
type testWeixinRegistrar struct{}

// NewTestWeixinRegistrar returns a WeixinRegistrar backed by the weixin plugin
// client, dialing the current test endpoint. Exposed so external (server_test)
// Deps builders can wire it too.
func NewTestWeixinRegistrar() WeixinRegistrar { return testWeixinRegistrar{} }

func (testWeixinRegistrar) GetQRCode() (WeixinQRCode, error) {
	qr, err := weixin.NewClient(weixinTestEndpoint, "", "", "").GetQRCode()
	if err != nil {
		return WeixinQRCode{}, err
	}
	return WeixinQRCode{QRCode: qr.QRCode, QRCodeImgContent: qr.QRCodeImgContent}, nil
}

func (testWeixinRegistrar) GetQRCodeStatus(qrcode string) (WeixinQRCodeStatus, error) {
	st, err := weixin.NewClient(weixinTestEndpoint, "", "", "").GetQRCodeStatus(qrcode)
	if err != nil {
		return WeixinQRCodeStatus{}, err
	}
	return WeixinQRCodeStatus{
		Status:      st.Status,
		BotToken:    st.BotToken,
		ILinkBotID:  st.ILinkBotID,
		ILinkUserID: st.ILinkUserID,
		BaseURL:     st.BaseURL,
	}, nil
}
