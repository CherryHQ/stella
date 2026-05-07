package feishu

import (
	"fmt"
	"io"
	"net/http"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/agent"
)

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

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}

	mime := http.DetectContentType(data)
	return data, mime, nil
}

// downloadFile downloads a file from Feishu and saves it to assetsDir.
// The filename is prefixed with a Unix timestamp to avoid collisions.
func (b *Bot) downloadFile(messageID, fileKey, fileName, assetsDir string) (string, error) {
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageResource.Get(apiCtx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type("file").
			Build())
	if err != nil {
		return "", fmt.Errorf("get resource: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return "", fmt.Errorf("empty file in response")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	return agent.SaveAsset(assetsDir, fileName, data)
}
