package weixin

import (
	"context"
	"crypto/md5" //nolint:gosec // MD5 is required by the WeChat CDN upload protocol
	"encoding/base64"
	"encoding/hex"

	"github.com/CherryHQ/stella/pkg/channel"
)

// sendFinalResponse delivers response text (via streaming when possible, otherwise
// chunked sendmessage), then sends any collected images.
func (b *Bot) sendFinalResponse(ctx context.Context, stream *channel.ChatStream, msg WeixinMessage, response string, images []channel.ImageEvent) error {
	if err := b.guard.AssertActive(); err != nil {
		return err
	}

	if err := b.sendViaStream(ctx, stream, msg, response); err != nil {
		return err
	}

	for _, img := range images {
		if err := b.sendImage(ctx, stream, msg, img); err != nil {
			return err
		}
	}
	return nil
}

// sendImage encrypts and uploads an image to CDN, then sends it as a message.
func (b *Bot) sendImage(ctx context.Context, stream *channel.ChatStream, msg WeixinMessage, img channel.ImageEvent) error {
	if err := b.guard.AssertActive(); err != nil {
		return err
	}
	data, err := decodeBase64(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return err
	}

	// Generate random AES key (16 bytes).
	key, keyHex := RandomFileKey(), ""
	keyBytes, err := hex.DecodeString(key)
	if err != nil {
		logger().Error("decode file key failed", "error", err)
		return err
	}
	keyHex = key // 32-char hex string for the aeskey field

	// Encrypt with AES-128-ECB.
	encrypted, err := EncryptAESECB(data, keyBytes)
	if err != nil {
		logger().Error("encrypt image failed", "error", err)
		return err
	}

	// Calculate MD5 of raw data.
	rawMD5 := md5Sum(data)
	fileKey := RandomFileKey()

	// Get upload URL.
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	uploadResp, err := b.client.GetUploadURL(UploadParams{
		FileKey:     fileKey,
		MediaType:   MediaTypeImage,
		ToUserID:    msg.FromUserID,
		RawSize:     len(data),
		RawFileMD5:  rawMD5,
		FileSize:    len(encrypted),
		NoNeedThumb: true,
		AESKey:      keyHex,
	})
	if err != nil {
		logger().Error("getuploadurl for image failed", "error", err)
		return err
	}

	// Upload to CDN.
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	encryptedParam, err := UploadToCDN("", uploadResp.UploadFullURL, uploadResp.UploadParam, fileKey, encrypted)
	if err != nil {
		logger().Error("cdn upload image failed", "error", err)
		return err
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
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	return b.client.SendMessage(reply)
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
