package weixin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/pkg/httpclient"
	"github.com/go-resty/resty/v2"
)

const (
	// DefaultBaseURL is the default iLink API base URL.
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"

	// DefaultCDNBaseURL is the CDN base URL for media upload/download.
	DefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com"

	// DefaultChannelVersion is the SDK/client version string.
	DefaultChannelVersion = "1.0.0"

	// authorizationType is the fixed value for the AuthorizationType header.
	authorizationType = "ilink_bot_token"

	// defaultTimeout is the default HTTP client timeout.
	defaultTimeout = 40 * time.Second
)

// ErrSessionExpired indicates the iLink session has expired (ret=-14 or errcode=-14).
var ErrSessionExpired = errors.New("weixin: session expired (ret=-14)")

// logger returns the package logger.
func logger() *slog.Logger { return slog.With("component", "weixin") }

// Client is an HTTP client for the iLink Bot API.
type Client struct {
	baseURL    string
	cdnBaseURL string
	token      string
	httpClient *resty.Client
}

// NewClient creates a new iLink API client.
func NewClient(baseURL, cdnBaseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if cdnBaseURL == "" {
		cdnBaseURL = DefaultCDNBaseURL
	}
	return &Client{
		baseURL:    baseURL,
		cdnBaseURL: cdnBaseURL,
		token:      token,
		httpClient: httpclient.NewWithTimeout(defaultTimeout),
	}
}

// randomWechatUIN generates a random X-WECHAT-UIN header value.
// Algorithm: random uint32 → decimal string → base64.
func randomWechatUIN() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	val := binary.BigEndian.Uint32(buf[:])
	dec := strconv.FormatUint(uint64(val), 10)
	return base64.StdEncoding.EncodeToString([]byte(dec))
}

// commonHeaders returns the required headers for all business POST requests.
func (c *Client) commonHeaders() map[string]string {
	return map[string]string{
		"Content-Type":      "application/json",
		"AuthorizationType": authorizationType,
		"Authorization":     "Bearer " + c.token,
		"X-WECHAT-UIN":      randomWechatUIN(),
	}
}

// GetQRCode requests a new QR code for login.
func (c *Client) GetQRCode(skRouteTag string) (*QRCodeResponse, error) {
	r := c.httpClient.R()
	if skRouteTag != "" {
		r.SetHeader("SKRouteTag", skRouteTag)
	}

	var result QRCodeResponse
	resp, err := r.SetResult(&result).Get(c.baseURL + "/ilink/bot/get_bot_qrcode?bot_type=3")
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: get qrcode: HTTP %d", resp.StatusCode())
	}
	return &result, nil
}

// GetQRCodeStatus polls the QR code scan status.
func (c *Client) GetQRCodeStatus(qrcode, skRouteTag string) (*QRCodeStatusResponse, error) {
	r := c.httpClient.R().
		SetHeader("iLink-App-ClientVersion", "1").
		SetQueryParam("qrcode", qrcode)
	if skRouteTag != "" {
		r.SetHeader("SKRouteTag", skRouteTag)
	}

	var result QRCodeStatusResponse
	resp, err := r.SetResult(&result).Get(c.baseURL + "/ilink/bot/get_qrcode_status")
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode status: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: get qrcode status: HTTP %d", resp.StatusCode())
	}
	return &result, nil
}

// GetUpdates performs a long-poll for new messages.
// Returns the response including messages, new cursor, and timeout hint.
func (c *Client) GetUpdates(buf, channelVersion string, timeout time.Duration) (*GetUpdatesResponse, error) {
	if channelVersion == "" {
		channelVersion = DefaultChannelVersion
	}

	body := GetUpdatesRequest{
		GetUpdatesBuf: buf,
		BaseInfo:      BaseInfo{ChannelVersion: channelVersion},
	}

	// Use a per-request client with adaptive timeout for long-polling.
	longPollClient := httpclient.NewWithTimeout(timeout)

	var result GetUpdatesResponse
	resp, err := longPollClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		Post(c.baseURL + "/ilink/bot/getupdates")
	if err != nil {
		return nil, fmt.Errorf("weixin: getupdates: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: getupdates: HTTP %d", resp.StatusCode())
	}

	if err := checkError(result.Ret, result.ErrCode, result.ErrMsg); err != nil {
		return nil, err
	}

	return &result, nil
}

// SendMessage sends a message to a user.
func (c *Client) SendMessage(msg WeixinMessage, channelVersion string) error {
	if channelVersion == "" {
		channelVersion = DefaultChannelVersion
	}

	body := SendMessageRequest{
		Msg:      msg,
		BaseInfo: BaseInfo{ChannelVersion: channelVersion},
	}

	var result SendMessageResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		Post(c.baseURL + "/ilink/bot/sendmessage")
	if err != nil {
		return fmt.Errorf("weixin: sendmessage: %w", err)
	}

	return checkError(result.Ret, result.ErrCode, result.ErrMsg)
}

// GetConfig retrieves the typing_ticket for a user.
func (c *Client) GetConfig(userID, contextToken, channelVersion string) (*GetConfigResponse, error) {
	if channelVersion == "" {
		channelVersion = DefaultChannelVersion
	}

	body := GetConfigRequest{
		ILinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     BaseInfo{ChannelVersion: channelVersion},
	}

	var result GetConfigResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		Post(c.baseURL + "/ilink/bot/getconfig")
	if err != nil {
		return nil, fmt.Errorf("weixin: getconfig: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: getconfig: HTTP %d", resp.StatusCode())
	}

	if err := checkError(result.Ret, result.ErrCode, result.ErrMsg); err != nil {
		return nil, err
	}

	return &result, nil
}

// SendTyping sends or cancels a typing indicator.
// status=1 starts typing, status=2 cancels.
func (c *Client) SendTyping(userID, typingTicket string, status int, channelVersion string) error {
	if channelVersion == "" {
		channelVersion = DefaultChannelVersion
	}

	body := SendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     BaseInfo{ChannelVersion: channelVersion},
	}

	var result SendTypingResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		Post(c.baseURL + "/ilink/bot/sendtyping")
	if err != nil {
		return fmt.Errorf("weixin: sendtyping: %w", err)
	}

	return checkError(result.Ret, result.ErrCode, result.ErrMsg)
}

// GetUploadURL requests CDN upload parameters for a file.
func (c *Client) GetUploadURL(params UploadParams, channelVersion string) (*GetUploadURLResponse, error) {
	if channelVersion == "" {
		channelVersion = DefaultChannelVersion
	}

	// Build the request body by embedding UploadParams fields + base_info.
	reqBody := struct {
		UploadParams
		BaseInfo BaseInfo `json:"base_info"`
	}{
		UploadParams: params,
		BaseInfo:     BaseInfo{ChannelVersion: channelVersion},
	}

	var result GetUploadURLResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(reqBody).
		SetResult(&result).
		Post(c.baseURL + "/ilink/bot/getuploadurl")
	if err != nil {
		return nil, fmt.Errorf("weixin: getuploadurl: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: getuploadurl: HTTP %d", resp.StatusCode())
	}

	if err := checkError(result.Ret, result.ErrCode, result.ErrMsg); err != nil {
		return nil, err
	}

	return &result, nil
}

// checkError returns ErrSessionExpired for ret=-14 or errcode=-14,
// a generic error for other non-zero codes, or nil on success.
func checkError(ret, errcode int, errmsg string) error {
	if ret == -14 || errcode == -14 {
		return ErrSessionExpired
	}
	if ret != 0 {
		return fmt.Errorf("weixin: API error ret=%d errcode=%d: %s", ret, errcode, errmsg)
	}
	if errcode != 0 {
		return fmt.Errorf("weixin: API error errcode=%d: %s", errcode, errmsg)
	}
	return nil
}
