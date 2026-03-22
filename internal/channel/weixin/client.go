package weixin

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
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
	httpClient *http.Client
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
		httpClient: &http.Client{Timeout: defaultTimeout},
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

// commonHeaders sets the required headers for all business POST requests.
func (c *Client) commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", authorizationType)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
}

// GetQRCode requests a new QR code for login.
func (c *Client) GetQRCode(skRouteTag string) (*QRCodeResponse, error) {
	u := c.baseURL + "/ilink/bot/get_bot_qrcode?bot_type=3"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: build qrcode request: %w", err)
	}
	if skRouteTag != "" {
		req.Header.Set("SKRouteTag", skRouteTag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode: %w", err)
	}
	defer resp.Body.Close()

	var result QRCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("weixin: decode qrcode response: %w", err)
	}
	return &result, nil
}

// GetQRCodeStatus polls the QR code scan status.
func (c *Client) GetQRCodeStatus(qrcode, skRouteTag string) (*QRCodeStatusResponse, error) {
	u := c.baseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: build qrcode status request: %w", err)
	}
	req.Header.Set("iLink-App-ClientVersion", "1")
	if skRouteTag != "" {
		req.Header.Set("SKRouteTag", skRouteTag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: get qrcode status: %w", err)
	}
	defer resp.Body.Close()

	var result QRCodeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("weixin: decode qrcode status: %w", err)
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

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("weixin: marshal getupdates: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/ilink/bot/getupdates", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("weixin: build getupdates request: %w", err)
	}
	c.commonHeaders(req)

	// Use a per-request client with adaptive timeout for long-polling.
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: getupdates: %w", err)
	}
	defer resp.Body.Close()

	var result GetUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("weixin: decode getupdates: %w", err)
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

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("weixin: marshal sendmessage: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/ilink/bot/sendmessage", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("weixin: build sendmessage request: %w", err)
	}
	c.commonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("weixin: sendmessage: %w", err)
	}
	defer resp.Body.Close()

	var result SendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// SendMessageResp may be empty on success.
		return nil
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

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("weixin: marshal getconfig: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/ilink/bot/getconfig", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("weixin: build getconfig request: %w", err)
	}
	c.commonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: getconfig: %w", err)
	}
	defer resp.Body.Close()

	var result GetConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("weixin: decode getconfig: %w", err)
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

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("weixin: marshal sendtyping: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/ilink/bot/sendtyping", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("weixin: build sendtyping request: %w", err)
	}
	c.commonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("weixin: sendtyping: %w", err)
	}
	defer resp.Body.Close()

	var result SendTypingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
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

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("weixin: marshal getuploadurl: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/ilink/bot/getuploadurl", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("weixin: build getuploadurl request: %w", err)
	}
	c.commonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: getuploadurl: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("weixin: read getuploadurl response: %w", err)
	}

	var result GetUploadURLResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("weixin: decode getuploadurl: %w", err)
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
