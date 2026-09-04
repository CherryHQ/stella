package feishu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/goldmark/feishucard"
)

// textContent builds the JSON content string for a Feishu text message.
func textContent(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

var (
	cardButtonDirective = regexp.MustCompile(`\{\{button\s+([^}]*)\}\}`)
	cardButtonAttr      = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

// stripCardDirectives rewrites {{button ...}} directives into readable plain
// text ("label: url") so a plain-text fallback never shows raw directives. Used
// when an interactive card can't be built and the reply must degrade to text.
func stripCardDirectives(text string) string {
	return cardButtonDirective.ReplaceAllStringFunc(text, func(m string) string {
		attrs := map[string]string{}
		for _, a := range cardButtonAttr.FindAllStringSubmatch(m, -1) {
			attrs[a[1]] = a[2]
		}
		switch {
		case attrs["label"] != "" && attrs["url"] != "":
			return attrs["label"] + ": " + attrs["url"]
		case attrs["url"] != "":
			return attrs["url"]
		default:
			return strings.TrimSpace(attrs["label"])
		}
	})
}

var buildCardContent = func(text string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": feishucard.Render(text),
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// cardContent builds a Feishu Interactive Card JSON 2.0 string.
// GFM tables become native table components; all other content is rendered
// as markdown elements. Cards support the Patch API for in-place editing.
func cardContent(text string) string {
	content, err := buildCardContent(text)
	if err != nil {
		return textContent(text)
	}
	return content
}

// feishuFileType maps common file extensions to Feishu's file_type field.
// Unrecognised extensions fall back to "stream" (generic binary).
func feishuFileType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return "mp4"
	case ".opus", ".mp3", ".wav", ".aac", ".amr", ".ogg":
		return "opus"
	default:
		return "stream"
	}
}

// feishuMessageTypeForFile chooses Feishu's native player messages when the
// stream supplies a recognised audio or video file. Every other extension
// keeps the generic file message behavior.
func feishuMessageTypeForFile(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".opus", ".mp3", ".wav", ".aac", ".amr", ".ogg":
		return "audio"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return "media"
	default:
		return larkim.MsgTypeFile
	}
}

// sendFile uploads a local file to Feishu and sends it as a file message reply.
func (b *Bot) sendFile(chatID, replyMsgID string, file channel.FileEvent, replyInThread bool) error {
	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	var fileKey string
	err := b.retryFeishuSend(uploadCtx, "upload file", func(ctx context.Context) error {
		f, openErr := os.Open(file.Path)
		if openErr != nil {
			return fmt.Errorf("open file %q: %w", file.Path, openErr)
		}
		defer func() { _ = f.Close() }()
		uploadResp, uploadErr := b.client.Im.File.Create(ctx,
			larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(feishuFileType(name)).
					FileName(name).
					File(f).
					Build()).
				Build())
		if uploadErr != nil {
			return uploadErr
		}
		if !uploadResp.Success() {
			return &feishuAPIError{code: uploadResp.Code, msg: uploadResp.Msg}
		}
		if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
			return fmt.Errorf("upload file %q: no file_key returned", name)
		}
		fileKey = *uploadResp.Data.FileKey
		return nil
	})
	if err != nil {
		return fmt.Errorf("upload file %q: %w", name, err)
	}
	if fileKey == "" {
		return fmt.Errorf("upload file %q: no file_key returned", name)
	}

	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	if err := b.retryFeishuSend(replyCtx, "send file", func(ctx context.Context) error {
		resp, replyErr := b.client.Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(replyMsgID).
				Body(replyMessageBody(feishuMessageTypeForFile(name), string(content), replyInThread)).
				Build())
		if replyErr != nil {
			return replyErr
		}
		if !resp.Success() {
			return &feishuAPIError{code: resp.Code, msg: resp.Msg}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("send file %q: %w", name, err)
	}
	return nil
}

// sendImage decodes a base64 image, uploads it to Feishu to obtain an image_key,
// then sends it as an image message in the chat.
func (b *Bot) sendImage(chatID, replyMsgID string, img channel.ImageEvent, replyInThread bool) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	var imageKey string
	if err := b.retryFeishuSend(uploadCtx, "upload image", func(ctx context.Context) error {
		uploadResp, uploadErr := b.client.Im.Image.Create(ctx,
			larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType("message").
					Image(bytes.NewReader(data)).
					Build()).
				Build())
		if uploadErr != nil {
			return uploadErr
		}
		if !uploadResp.Success() {
			return &feishuAPIError{code: uploadResp.Code, msg: uploadResp.Msg}
		}
		if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
			return fmt.Errorf("upload image: no image_key returned")
		}
		imageKey = *uploadResp.Data.ImageKey
		return nil
	}); err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	if imageKey == "" {
		return fmt.Errorf("upload image: no image_key returned")
	}

	// Send image message as a reply.
	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	if err := b.retryFeishuSend(replyCtx, "send image", func(ctx context.Context) error {
		resp, replyErr := b.client.Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(replyMsgID).
				Body(replyMessageBody(larkim.MsgTypeImage, string(content), replyInThread)).
				Build())
		if replyErr != nil {
			return replyErr
		}
		if !resp.Success() {
			return &feishuAPIError{code: resp.Code, msg: resp.Msg}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("send image: %w", err)
	}
	return nil
}
