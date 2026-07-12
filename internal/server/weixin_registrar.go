package server

// WeixinQRCode is the server-local result of a WeChat (iLink) QR-code request.
// Its JSON tags mirror the wire contract the QR endpoints have always emitted so
// the HTTP response body is unchanged after routing through the registrar port.
type WeixinQRCode struct {
	QRCode           string `json:"qrcode,omitempty"`
	QRCodeImgContent string `json:"qrcode_img_content,omitempty"`
}

// WeixinQRCodeStatus is the server-local result of a WeChat (iLink) QR-code scan
// poll. Its JSON tags mirror the existing wire contract for PollWeixinQRStatus.
type WeixinQRCodeStatus struct {
	Status      string `json:"status,omitempty"`
	BotToken    string `json:"bot_token,omitempty"`
	ILinkBotID  string `json:"ilink_bot_id,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
	BaseURL     string `json:"baseurl,omitempty"`
}

// WeixinRegistrar is the narrow port the WeChat registration/QR handlers use to
// reach the iLink API. The concrete adapter lives in the composition root (it
// wraps the weixin plugin's client), so internal/server never imports the
// plugin. Results are mapped to the server-local types above at the boundary.
type WeixinRegistrar interface {
	// GetQRCode requests a fresh login QR code from the iLink API.
	GetQRCode() (WeixinQRCode, error)
	// GetQRCodeStatus polls the scan status of a previously issued QR code.
	GetQRCodeStatus(qrcode string) (WeixinQRCodeStatus, error)
}
