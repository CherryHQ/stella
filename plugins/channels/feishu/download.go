package feishu

import (
	"fmt"
	"io"
	"net/http"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// maxImageBytes caps a single downloaded image. base64-encoding holds ~1.33x of
// this in memory on top of the raw bytes, so the limit bounds per-message peak
// memory even when a post references many inline images.
const maxImageBytes = 20 << 20 // 20 MiB

// downloadImage downloads an image from Feishu using the MessageResource API.
func (b *Bot) downloadImage(messageID, imageKey string) ([]byte, string, error) {
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageResource.Get(apiCtx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build())
	if err != nil {
		return nil, "", fmt.Errorf("get resource: %w", err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return nil, "", fmt.Errorf("empty file in response")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	data, err := io.ReadAll(io.LimitReader(resp.File, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}

	mime := http.DetectContentType(data)
	return data, mime, nil
}

// downloadFile downloads a file from Feishu and saves it to assetsDir. It
// returns the saved path together with the raw bytes so callers can present
// image files inline without re-reading the file.
// The filename is prefixed with a Unix timestamp to avoid collisions.
func (b *Bot) downloadFile(messageID, fileKey, fileName, assetsDir string) (string, []byte, error) {
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageResource.Get(apiCtx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type("file").
			Build())
	if err != nil {
		return "", nil, fmt.Errorf("get resource: %w", err)
	}
	if !resp.Success() {
		return "", nil, fmt.Errorf("api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return "", nil, fmt.Errorf("empty file in response")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return "", nil, fmt.Errorf("read file: %w", err)
	}

	path, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
	if err != nil {
		return "", nil, err
	}
	return path, data, nil
}
