package weixin

// Message type constants.
const (
	MessageTypeUser = 1 // USER — incoming message from a WeChat user.
	MessageTypeBot  = 2 // BOT — outgoing message from the bot.
)

// Message state constants.
const (
	MessageStateNew        = 0 // NEW — initial state.
	MessageStateGenerating = 1 // GENERATING — content being generated (not used for sending).
	MessageStateFinish     = 2 // FINISH — final state; always use this for outbound messages.
)

// Item type constants.
const (
	ItemTypeUnsupported = 0
	ItemTypeText        = 1
	ItemTypeImage       = 2
	ItemTypeVoice       = 3
	ItemTypeFile        = 4
	ItemTypeVideo       = 5
)

// Media type constants for getuploadurl.
const (
	MediaTypeImage = 1
	MediaTypeVideo = 2
	MediaTypeFile  = 3
	MediaTypeVoice = 4
)

// WeixinMessage represents a single message in the iLink Bot protocol.
type WeixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64         `json:"update_time_ms,omitempty"`
	DeleteTimeMS int64         `json:"delete_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

// MessageItem represents a single content item within a message.
type MessageItem struct {
	Type         int         `json:"type,omitempty"`
	CreateTimeMS int64       `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64       `json:"update_time_ms,omitempty"`
	IsCompleted  *bool       `json:"is_completed,omitempty"`
	MsgID        string      `json:"msg_id,omitempty"`
	RefMsg       *RefMessage `json:"ref_msg,omitempty"`
	TextItem     *TextItem   `json:"text_item,omitempty"`
	ImageItem    *ImageItem  `json:"image_item,omitempty"`
	VoiceItem    *VoiceItem  `json:"voice_item,omitempty"`
	FileItem     *FileItem   `json:"file_item,omitempty"`
	VideoItem    *VideoItem  `json:"video_item,omitempty"`
}

// TextItem holds text content.
type TextItem struct {
	Text string `json:"text,omitempty"`
}

// ImageItem holds image content and CDN references.
type ImageItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"` // hex string, 32 chars
	URL         string    `json:"url,omitempty"`
	MidSize     int64     `json:"mid_size,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	HDSize      int64     `json:"hd_size,omitempty"`
}

// VoiceItem holds voice content.
type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	Playtime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

// FileItem holds file content.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"` // plaintext file size as string
}

// VideoItem holds video content.
type VideoItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	VideoSize   int64     `json:"video_size,omitempty"`
	PlayLength  int       `json:"play_length,omitempty"`
	VideoMD5    string    `json:"video_md5,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
}

// CDNMedia is the shared CDN reference for images, voice, files, and videos.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"` // base64 encoded
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// RefMessage represents a quoted/referenced message.
type RefMessage struct {
	Title       string       `json:"title,omitempty"`
	MessageItem *MessageItem `json:"message_item,omitempty"`
}

// BaseInfo is included in every business POST request body.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// --- API request/response types ---

// GetUpdatesRequest is the request body for getupdates.
type GetUpdatesRequest struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      BaseInfo `json:"base_info"`
}

// GetUpdatesResponse is the response body for getupdates.
type GetUpdatesResponse struct {
	Ret                  int             `json:"ret"`
	ErrCode              int             `json:"errcode,omitempty"`
	ErrMsg               string          `json:"errmsg,omitempty"`
	Msgs                 []WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMS int             `json:"longpolling_timeout_ms,omitempty"`
}

// SendMessageRequest is the request body for sendmessage.
type SendMessageRequest struct {
	Msg      WeixinMessage `json:"msg"`
	BaseInfo BaseInfo      `json:"base_info"`
}

// SendMessageResponse is the response body for sendmessage.
type SendMessageResponse struct {
	Ret     int    `json:"ret,omitempty"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

// GetConfigRequest is the request body for getconfig.
type GetConfigRequest struct {
	ILinkUserID  string   `json:"ilink_user_id"`
	ContextToken string   `json:"context_token,omitempty"`
	BaseInfo     BaseInfo `json:"base_info"`
}

// GetConfigResponse is the response body for getconfig.
type GetConfigResponse struct {
	Ret          int    `json:"ret"`
	ErrCode      int    `json:"errcode,omitempty"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// SendTypingRequest is the request body for sendtyping.
type SendTypingRequest struct {
	ILinkUserID  string   `json:"ilink_user_id"`
	TypingTicket string   `json:"typing_ticket"`
	Status       int      `json:"status"`
	BaseInfo     BaseInfo `json:"base_info"`
}

// SendTypingResponse is the response body for sendtyping.
type SendTypingResponse struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

// UploadParams holds the parameters for getuploadurl.
type UploadParams struct {
	FileKey       string `json:"filekey"`
	MediaType     int    `json:"media_type"`
	ToUserID      string `json:"to_user_id"`
	RawSize       int    `json:"rawsize"`
	RawFileMD5    string `json:"rawfilemd5"`
	FileSize      int    `json:"filesize"`
	NoNeedThumb   bool   `json:"no_need_thumb,omitempty"`
	AESKey        string `json:"aeskey,omitempty"` // hex string
	ThumbRawSize  int    `json:"thumb_rawsize,omitempty"`
	ThumbRawMD5   string `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize int    `json:"thumb_filesize,omitempty"`
}

// GetUploadURLRequest is the request body for getuploadurl.
type GetUploadURLRequest struct {
	UploadParams
	BaseInfo BaseInfo `json:"base_info"`
}

// GetUploadURLResponse is the response body for getuploadurl.
type GetUploadURLResponse struct {
	Ret              int    `json:"ret,omitempty"`
	ErrCode          int    `json:"errcode,omitempty"`
	ErrMsg           string `json:"errmsg,omitempty"`
	UploadParam      string `json:"upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
}

// NotifyRequest is the request body for notifystart and notifystop.
type NotifyRequest struct {
	BaseInfo BaseInfo `json:"base_info"`
}

// NotifyResponse is the response body for notifystart and notifystop.
type NotifyResponse struct {
	Ret    int    `json:"ret,omitempty"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// --- Streaming API types ---

const streamBusinessType = 10

// InitStreamRequest is the request body for init_stream.
type InitStreamRequest struct {
	DeviceID       string   `json:"device_id"`
	ClientStreamID string   `json:"client_stream_id"`
	BusinessType   int      `json:"business_type"`
	BaseInfo       BaseInfo `json:"base_info"`
}

// BaseResponse is the error envelope used by the streaming API responses.
type BaseResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// InitStreamResponse is the response body for init_stream.
type InitStreamResponse struct {
	BaseResponse *BaseResponse `json:"base_response,omitempty"`
	StreamTicket string        `json:"stream_ticket,omitempty"`
}

// SyncStreamPiece is a single piece in a SyncStream request.
// PieceData is a base64-encoded JSON object: {"type":"text","text":"...","stream_type":"text"}.
type SyncStreamPiece struct {
	PieceSeq  int    `json:"piece_seq"`
	PieceData string `json:"piece_data"`
}

// SyncStreamRequest is the request body for sync_stream.
type SyncStreamRequest struct {
	DeviceID       string            `json:"device_id"`
	ClientStreamID string            `json:"client_stream_id"`
	BusinessType   int               `json:"business_type"`
	UpPieceList    []SyncStreamPiece `json:"up_piece_list"`
	EndUpPieceSeq  int               `json:"end_up_piece_seq"`
	StreamTicket   string            `json:"stream_ticket"`
	BaseInfo       BaseInfo          `json:"base_info"`
}

// SyncStreamAbortInfo carries server-side abort details.
type SyncStreamAbortInfo struct {
	AbortType            int    `json:"abort_type"`
	AbortDetailErrorCode int    `json:"abort_detail_error_code"`
	AbortDetailErrorMsg  string `json:"abort_detail_error_msg,omitempty"`
}

// SyncStreamResponse is the response body for sync_stream.
type SyncStreamResponse struct {
	BaseResponse *BaseResponse        `json:"base_response,omitempty"`
	AbortInfo    *SyncStreamAbortInfo `json:"abort_info,omitempty"`
}

// QRCodeResponse is the response from get_bot_qrcode.
type QRCodeResponse struct {
	QRCode           string `json:"qrcode,omitempty"`
	QRCodeImgContent string `json:"qrcode_img_content,omitempty"`
}

// QRCodeStatusResponse is the response from get_qrcode_status.
type QRCodeStatusResponse struct {
	Status      string `json:"status,omitempty"`
	BotToken    string `json:"bot_token,omitempty"`
	ILinkBotID  string `json:"ilink_bot_id,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
	BaseURL     string `json:"baseurl,omitempty"`
}
