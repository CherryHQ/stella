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
	case ".mp4":
		return "mp4"
	case ".opus":
		return "opus"
	default:
		return "stream"
	}
}

// sendFile uploads a local file to Feishu and sends it as a file message reply.
func (b *Bot) sendFile(ctx context.Context, chatID, replyMsgID string, file channel.FileEvent, replyInThread bool, stream *channel.ChatStream) error {
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	f, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("open file %q: %w", file.Path, err)
	}
	defer func() { _ = f.Close() }()

	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	uploadResp, err := b.client.Im.File.Create(uploadCtx,
		larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(feishuFileType(name)).
				FileName(name).
				File(f).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("upload file %q outcome unknown: %w", name, err)
	}
	if !uploadResp.Success() {
		return fmt.Errorf("upload file api error: code=%d msg=%s", uploadResp.Code, uploadResp.Msg)
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("upload file: no file_key returned")
	}

	fileKey := *uploadResp.Data.FileKey

	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	resp, err := b.client.Im.Message.Reply(replyCtx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(replyMessageBody(larkim.MsgTypeFile, string(content), replyInThread)).
			Build())
	if err != nil {
		return fmt.Errorf("send file outcome unknown: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("send file api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// sendImage decodes a base64 image, uploads it to Feishu to obtain an image_key,
// then sends it as an image message in the chat.
func (b *Bot) sendImage(ctx context.Context, chatID, replyMsgID string, img channel.ImageEvent, replyInThread bool, stream *channel.ChatStream) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	// Upload image to get image_key.
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	uploadResp, err := b.client.Im.Image.Create(uploadCtx,
		larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType("message").
				Image(bytes.NewReader(data)).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("upload image outcome unknown: %w", err)
	}
	if !uploadResp.Success() {
		return fmt.Errorf("upload image api error: code=%d msg=%s", uploadResp.Code, uploadResp.Msg)
	}
	if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
		return fmt.Errorf("upload image: no image_key returned")
	}

	imageKey := *uploadResp.Data.ImageKey

	// Send image message as a reply.
	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	resp, err := b.client.Im.Message.Reply(replyCtx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(replyMessageBody(larkim.MsgTypeImage, string(content), replyInThread)).
			Build())
	if err != nil {
		return fmt.Errorf("send image outcome unknown: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("send image api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
