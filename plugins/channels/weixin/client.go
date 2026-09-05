package weixin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

const (
	// DefaultBaseURL is the default iLink API base URL.
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"

	// DefaultCDNBaseURL is the CDN base URL for media upload/download.
	DefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com"

	// authorizationType is the fixed value for the AuthorizationType header.
	authorizationType = "ilink_bot_token"

	// iLinkAppID is the fixed iLink application identifier for bot clients.
	iLinkAppID = "bot"

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
	skRouteTag string
	version    string
	httpClient *resty.Client
}

// NewClient creates a new iLink API client.
// skRouteTag is an optional routing hint sent via the SKRouteTag header; pass "" to omit it.
func NewClient(baseURL, cdnBaseURL, token, skRouteTag, version string) *Client {
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
		skRouteTag: skRouteTag,
		version:    version,
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

// buildClientVersion encodes a semver string as the iLink uint32 wire format:
// high 8 bits = 0, then major<<16 | minor<<8 | patch. Each component is clamped to 0xFF.
// Returns 0 for non-semver strings (e.g. "dev").
func buildClientVersion(v string) uint32 {
	parts := strings.SplitN(v, ".", 3)
	parse := func(s string) uint32 {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return 0
		}
		return uint32(n) & 0xff
	}
	var major, minor, patch uint32
	if len(parts) > 0 {
		major = parse(parts[0])
	}
	if len(parts) > 1 {
		minor = parse(parts[1])
	}
	if len(parts) > 2 {
		patch = parse(parts[2])
	}
	return (major << 16) | (minor << 8) | patch
}

// buildBaseInfo returns a populated BaseInfo for all POST request bodies.
func (c *Client) buildBaseInfo() BaseInfo {
	v := c.version
	// When stella is built without ldflags version injection (dev builds), use the
	// official plugin's DEFAULT_BOT_AGENT so backend attribution is meaningful.
	botAgent := "OpenClaw"
	if v != "" {
		botAgent = "Stella/" + v
	} else {
		v = "dev"
	}
	return BaseInfo{
		ChannelVersion: v,
		BotAgent:       botAgent,
	}
}

// commonHeaders returns the required headers for all iLink API requests.
func (c *Client) commonHeaders() map[string]string {
	headers := map[string]string{
		"Content-Type":            "application/json",
		"AuthorizationType":       authorizationType,
		"Authorization":           "Bearer " + c.token,
		"X-WECHAT-UIN":            randomWechatUIN(),
		"iLink-App-Id":            iLinkAppID,
		"iLink-App-ClientVersion": strconv.FormatUint(uint64(buildClientVersion(c.version)), 10),
	}
	if c.skRouteTag != "" {
		headers["SKRouteTag"] = c.skRouteTag
	}
	return headers
}

// GetQRCode requests a new QR code for login.
func (c *Client) GetQRCode() (*QRCodeResponse, error) {
	// iLink returns Content-Type: application/octet-stream even for JSON bodies,
	// so we read the raw body and unmarshal manually.
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		Get(c.baseURL + "/ilink/bot/get_bot_qrcode?bot_type=3")
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: get qrcode: HTTP %d", resp.StatusCode())
	}

	var result struct {
		Ret              int    `json:"ret"`
		ErrCode          int    `json:"errcode,omitempty"`
		ErrMsg           string `json:"errmsg,omitempty"`
		QRCode           string `json:"qrcode,omitempty"`
		QRCodeImgContent string `json:"qrcode_img_content,omitempty"`
	}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("weixin: get qrcode: decode response: %w", err)
	}
	if err := checkError(result.Ret, result.ErrCode, result.ErrMsg); err != nil {
		return nil, err
	}
	return &QRCodeResponse{
		QRCode:           result.QRCode,
		QRCodeImgContent: result.QRCodeImgContent,
	}, nil
}

// GetQRCodeStatus polls the QR code scan status.
func (c *Client) GetQRCodeStatus(qrcode string) (*QRCodeStatusResponse, error) {
	// iLink returns Content-Type: application/octet-stream even for JSON bodies.
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetQueryParam("qrcode", qrcode).
		Get(c.baseURL + "/ilink/bot/get_qrcode_status")
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode status: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: get qrcode status: HTTP %d", resp.StatusCode())
	}

	var result struct {
		Ret         int    `json:"ret"`
		ErrCode     int    `json:"errcode,omitempty"`
		ErrMsg      string `json:"errmsg,omitempty"`
		Status      string `json:"status,omitempty"`
		BotToken    string `json:"bot_token,omitempty"`
		ILinkBotID  string `json:"ilink_bot_id,omitempty"`
		ILinkUserID string `json:"ilink_user_id,omitempty"`
		BaseURL     string `json:"baseurl,omitempty"`
	}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("weixin: get qrcode status: decode response: %w", err)
	}
	if err := checkError(result.Ret, result.ErrCode, result.ErrMsg); err != nil {
		return nil, err
	}
	return &QRCodeStatusResponse{
		Status:      result.Status,
		BotToken:    result.BotToken,
		ILinkBotID:  result.ILinkBotID,
		ILinkUserID: result.ILinkUserID,
		BaseURL:     result.BaseURL,
	}, nil
}

// GetUpdates performs a long-poll for new messages.
// Returns the response including messages, new cursor, and timeout hint.
func (c *Client) GetUpdates(buf string, timeout time.Duration) (*GetUpdatesResponse, error) {
	body := GetUpdatesRequest{
		GetUpdatesBuf: buf,
		BaseInfo:      c.buildBaseInfo(),
	}

	// Use a per-request client with adaptive timeout for long-polling.
	longPollClient := httpclient.NewWithTimeout(timeout)

	var result GetUpdatesResponse
	resp, err := longPollClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
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
func (c *Client) SendMessage(msg WeixinMessage) error {
	body := SendMessageRequest{
		Msg:      msg,
		BaseInfo: c.buildBaseInfo(),
	}

	var result SendMessageResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/sendmessage")
	if err != nil {
		return fmt.Errorf("weixin: sendmessage: %w", err)
	}

	return checkError(result.Ret, result.ErrCode, result.ErrMsg)
}

// GetConfig retrieves the typing_ticket for a user.
func (c *Client) GetConfig(userID, contextToken string) (*GetConfigResponse, error) {
	body := GetConfigRequest{
		ILinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     c.buildBaseInfo(),
	}

	var result GetConfigResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
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
func (c *Client) SendTyping(userID, typingTicket string, status int) error {
	body := SendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     c.buildBaseInfo(),
	}

	var result SendTypingResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/sendtyping")
	if err != nil {
		return fmt.Errorf("weixin: sendtyping: %w", err)
	}

	return checkError(result.Ret, result.ErrCode, result.ErrMsg)
}

// GetUploadURL requests CDN upload parameters for a file.
func (c *Client) GetUploadURL(params UploadParams) (*GetUploadURLResponse, error) {
	reqBody := struct {
		UploadParams
		BaseInfo BaseInfo `json:"base_info"`
	}{
		UploadParams: params,
		BaseInfo:     c.buildBaseInfo(),
	}

	var result GetUploadURLResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(reqBody).
		SetResult(&result).
		ForceContentType("application/json").
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

// NotifyStart notifies the iLink backend that this bot is starting.
func (c *Client) NotifyStart() error {
	body := NotifyRequest{BaseInfo: c.buildBaseInfo()}

	var result NotifyResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/msg/notifystart")
	if err != nil {
		return fmt.Errorf("weixin: notifystart: %w", err)
	}
	if result.Ret != 0 {
		return fmt.Errorf("weixin: notifystart: ret=%d: %s", result.Ret, result.ErrMsg)
	}
	return nil
}

// NotifyStop notifies the iLink backend that this bot is stopping.
// Uses a standalone call so the request can finish even during shutdown.
func (c *Client) NotifyStop() error {
	body := NotifyRequest{BaseInfo: c.buildBaseInfo()}

	var result NotifyResponse
	_, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/msg/notifystop")
	if err != nil {
		return fmt.Errorf("weixin: notifystop: %w", err)
	}
	if result.Ret != 0 {
		return fmt.Errorf("weixin: notifystop: ret=%d: %s", result.Ret, result.ErrMsg)
	}
	return nil
}

// InitStream initialises a new uplink stream and returns the stream_ticket.
// device_id identifies the bot device; client_stream_id is a unique session ID.
func (c *Client) InitStream(deviceID, clientStreamID string) (*InitStreamResponse, error) {
	body := InitStreamRequest{
		DeviceID:       deviceID,
		ClientStreamID: clientStreamID,
		BusinessType:   streamBusinessType,
		BaseInfo:       c.buildBaseInfo(),
	}

	var result InitStreamResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(body).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/stream/init_stream")
	if err != nil {
		return nil, fmt.Errorf("weixin: init_stream: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("weixin: init_stream: HTTP %d", resp.StatusCode())
	}
	if result.BaseResponse != nil && result.BaseResponse.Ret != 0 {
		return nil, fmt.Errorf("weixin: init_stream: ret=%d: %s", result.BaseResponse.Ret, result.BaseResponse.ErrMsg)
	}
	if result.StreamTicket == "" {
		return nil, fmt.Errorf("weixin: init_stream: no stream_ticket in response")
	}
	return &result, nil
}

// SyncStream sends one batch of pieces on an active stream.
// Set EndUpPieceSeq to the last piece's PieceSeq to signal end-of-stream; 0 for intermediate.
func (c *Client) SyncStream(req SyncStreamRequest) error {
	req.BaseInfo = c.buildBaseInfo()
	req.BusinessType = streamBusinessType

	var result SyncStreamResponse
	resp, err := c.httpClient.R().
		SetHeaders(c.commonHeaders()).
		SetBody(req).
		SetResult(&result).
		ForceContentType("application/json").
		Post(c.baseURL + "/ilink/bot/stream/sync_stream")
	if err != nil {
		return fmt.Errorf("weixin: sync_stream: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("weixin: sync_stream: HTTP %d", resp.StatusCode())
	}
	if result.AbortInfo != nil && result.AbortInfo.AbortType != 0 {
		return fmt.Errorf("weixin: sync_stream: server abort type=%d code=%d: %s",
			result.AbortInfo.AbortType, result.AbortInfo.AbortDetailErrorCode, result.AbortInfo.AbortDetailErrorMsg)
	}
	if result.BaseResponse != nil && result.BaseResponse.Ret != 0 {
		return fmt.Errorf("weixin: sync_stream: ret=%d: %s", result.BaseResponse.Ret, result.BaseResponse.ErrMsg)
	}
	return nil
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
