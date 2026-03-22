package weixin

import (
	"crypto/md5" //nolint:gosec // MD5 is required by the WeChat CDN upload protocol
	"encoding/base64"
	"encoding/hex"
	"strconv"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
)

// sendFinalResponse splits text at 2000 chars and sends each chunk,
// then sends any collected images.
func (b *Bot) sendFinalResponse(msg WeixinMessage, response string, images []runner.ImageEvent) {
	chunks := channel.SplitMessage(response, weixinMaxMessageLen)

	contextToken := ""
	if v, ok := b.contextTokens.Load(msg.FromUserID); ok {
		contextToken, _ = v.(string)
	}

	for _, chunk := range chunks {
		reply := WeixinMessage{
			ToUserID:     msg.FromUserID,
			ClientID:     RandomClientID("resp"),
			MessageType:  MessageTypeBot,
			MessageState: MessageStateFinish,
			ContextToken: contextToken,
			ItemList: []MessageItem{
				{
					Type:     ItemTypeText,
					TextItem: &TextItem{Text: chunk},
				},
			},
		}
		if err := b.client.SendMessage(reply, ""); err != nil {
			logger().Error("send response chunk failed", "user_id", msg.FromUserID, "error", err)
		}
	}

	for _, img := range images {
		b.sendImage(msg, img)
	}
}

// sendImage encrypts and uploads an image to CDN, then sends it as a message.
func (b *Bot) sendImage(msg WeixinMessage, img runner.ImageEvent) {
	data, err := decodeBase64(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return
	}

	// Generate random AES key (16 bytes).
	key, keyHex := RandomFileKey(), ""
	keyBytes, err := hex.DecodeString(key)
	if err != nil {
		logger().Error("decode file key failed", "error", err)
		return
	}
	keyHex = key // 32-char hex string for the aeskey field

	// Encrypt with AES-128-ECB.
	encrypted, err := EncryptAESECB(data, keyBytes)
	if err != nil {
		logger().Error("encrypt image failed", "error", err)
		return
	}

	// Calculate MD5 of raw data.
	rawMD5 := md5Sum(data)
	fileKey := RandomFileKey()

	// Get upload URL.
	uploadResp, err := b.client.GetUploadURL(UploadParams{
		FileKey:     fileKey,
		MediaType:   MediaTypeImage,
		ToUserID:    msg.FromUserID,
		RawSize:     len(data),
		RawFileMD5:  rawMD5,
		FileSize:    len(encrypted),
		NoNeedThumb: true,
		AESKey:      keyHex,
	}, "")
	if err != nil {
		logger().Error("getuploadurl for image failed", "error", err)
		return
	}

	// Upload to CDN.
	encryptedParam, err := UploadToCDN("", uploadResp.UploadParam, fileKey, encrypted)
	if err != nil {
		logger().Error("cdn upload image failed", "error", err)
		return
	}

	// Send image message.
	contextToken := ""
	if v, ok := b.contextTokens.Load(msg.FromUserID); ok {
		contextToken, _ = v.(string)
	}

	reply := WeixinMessage{
		ToUserID:     msg.FromUserID,
		ClientID:     RandomClientID("img"),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type: ItemTypeImage,
				ImageItem: &ImageItem{
					Media: &CDNMedia{
						EncryptQueryParam: encryptedParam,
						AESKey:            base64.StdEncoding.EncodeToString([]byte(keyHex)),
						EncryptType:       1,
					},
					AESKey:  keyHex,
					MidSize: int64(len(encrypted)),
				},
			},
		},
	}
	if err := b.client.SendMessage(reply, ""); err != nil {
		logger().Error("send image message failed", "user_id", msg.FromUserID, "error", err)
	}
}

// sendFile encrypts and uploads a file to CDN, then sends it as a message.
func (b *Bot) sendFile(msg WeixinMessage, fileName string, data []byte) {
	key, keyHex := RandomFileKey(), ""
	keyBytes, err := hex.DecodeString(key)
	if err != nil {
		logger().Error("decode file key failed", "error", err)
		return
	}
	keyHex = key

	encrypted, err := EncryptAESECB(data, keyBytes)
	if err != nil {
		logger().Error("encrypt file failed", "error", err)
		return
	}

	rawMD5 := md5Sum(data)
	fileKey := RandomFileKey()

	uploadResp, err := b.client.GetUploadURL(UploadParams{
		FileKey:     fileKey,
		MediaType:   MediaTypeFile,
		ToUserID:    msg.FromUserID,
		RawSize:     len(data),
		RawFileMD5:  rawMD5,
		FileSize:    len(encrypted),
		NoNeedThumb: true,
		AESKey:      keyHex,
	}, "")
	if err != nil {
		logger().Error("getuploadurl for file failed", "error", err)
		return
	}

	encryptedParam, err := UploadToCDN("", uploadResp.UploadParam, fileKey, encrypted)
	if err != nil {
		logger().Error("cdn upload file failed", "error", err)
		return
	}

	contextToken := ""
	if v, ok := b.contextTokens.Load(msg.FromUserID); ok {
		contextToken, _ = v.(string)
	}

	reply := WeixinMessage{
		ToUserID:     msg.FromUserID,
		ClientID:     RandomClientID("file"),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type: ItemTypeFile,
				FileItem: &FileItem{
					Media: &CDNMedia{
						EncryptQueryParam: encryptedParam,
						AESKey:            base64.StdEncoding.EncodeToString([]byte(keyHex)),
						EncryptType:       1,
					},
					FileName: fileName,
					Len:      strconv.Itoa(len(data)),
				},
			},
		},
	}
	if err := b.client.SendMessage(reply, ""); err != nil {
		logger().Error("send file message failed", "user_id", msg.FromUserID, "error", err)
	}
}

// sendVideo encrypts and uploads a video to CDN, then sends it as a message.
func (b *Bot) sendVideo(msg WeixinMessage, data []byte) {
	key, keyHex := RandomFileKey(), ""
	keyBytes, err := hex.DecodeString(key)
	if err != nil {
		logger().Error("decode file key failed", "error", err)
		return
	}
	keyHex = key

	encrypted, err := EncryptAESECB(data, keyBytes)
	if err != nil {
		logger().Error("encrypt video failed", "error", err)
		return
	}

	rawMD5 := md5Sum(data)
	fileKey := RandomFileKey()

	uploadResp, err := b.client.GetUploadURL(UploadParams{
		FileKey:     fileKey,
		MediaType:   MediaTypeVideo,
		ToUserID:    msg.FromUserID,
		RawSize:     len(data),
		RawFileMD5:  rawMD5,
		FileSize:    len(encrypted),
		NoNeedThumb: true,
		AESKey:      keyHex,
	}, "")
	if err != nil {
		logger().Error("getuploadurl for video failed", "error", err)
		return
	}

	encryptedParam, err := UploadToCDN("", uploadResp.UploadParam, fileKey, encrypted)
	if err != nil {
		logger().Error("cdn upload video failed", "error", err)
		return
	}

	contextToken := ""
	if v, ok := b.contextTokens.Load(msg.FromUserID); ok {
		contextToken, _ = v.(string)
	}

	reply := WeixinMessage{
		ToUserID:     msg.FromUserID,
		ClientID:     RandomClientID("video"),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type: ItemTypeVideo,
				VideoItem: &VideoItem{
					Media: &CDNMedia{
						EncryptQueryParam: encryptedParam,
						AESKey:            base64.StdEncoding.EncodeToString([]byte(keyHex)),
						EncryptType:       1,
					},
					VideoSize: int64(len(encrypted)),
				},
			},
		},
	}
	if err := b.client.SendMessage(reply, ""); err != nil {
		logger().Error("send video message failed", "user_id", msg.FromUserID, "error", err)
	}
}

// --- helpers ---

// decodeBase64 decodes a base64-encoded string, trying standard then URL encoding.
func decodeBase64(s string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// md5Sum returns the hex-encoded MD5 of data.
func md5Sum(data []byte) string {
	h := md5.Sum(data) //nolint:gosec // MD5 is required by WeChat CDN protocol
	return hex.EncodeToString(h[:])
}
