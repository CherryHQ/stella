package feishu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/pkg/channel"
)

// textContent builds the JSON content string for a Feishu text message.
func textContent(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

// cardContent builds a Feishu Interactive Card JSON 2.0 string with markdown content.
// Cards support the Patch API for in-place editing (plain text messages do not).
func cardContent(text string) string {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": text,
				},
			},
		},
	}
	data, _ := json.Marshal(card)
	return string(data)
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
func (b *Bot) sendFile(chatID, replyMsgID string, file channel.FileEvent) {
	f, err := os.Open(file.Path)
	if err != nil {
		logger().Error("open file failed", "path", file.Path, "error", err)
		return
	}
	defer func() { _ = f.Close() }()

	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	uploadResp, err := b.client.Im.File.Create(uploadCtx,
		larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(feishuFileType(name)).
				FileName(name).
				File(f).
				Build()).
			Build())
	if err != nil {
		logger().Error("upload file failed", "name", name, "error", err)
		return
	}
	if !uploadResp.Success() {
		logger().Error("upload file api error", "code", uploadResp.Code, "msg", uploadResp.Msg)
		return
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		logger().Error("upload file: no file_key returned")
		return
	}

	fileKey := *uploadResp.Data.FileKey

	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	resp, err := b.client.Im.Message.Reply(replyCtx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeFile).
				Content(string(content)).
				Build()).
			Build())
	if err != nil {
		logger().Error("send file failed", "error", err)
		return
	}
	if !resp.Success() {
		logger().Error("send file api error", "code", resp.Code, "msg", resp.Msg)
	}
}

// sendImage decodes a base64 image, uploads it to Feishu to obtain an image_key,
// then sends it as an image message in the chat.
func (b *Bot) sendImage(chatID, replyMsgID string, img channel.ImageEvent) {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	// Upload image to get image_key.
	uploadResp, err := b.client.Im.Image.Create(uploadCtx,
		larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType("message").
				Image(bytes.NewReader(data)).
				Build()).
			Build())
	if err != nil {
		logger().Error("upload image failed", "error", err)
		return
	}
	if !uploadResp.Success() {
		logger().Error("upload image api error", "code", uploadResp.Code, "msg", uploadResp.Msg)
		return
	}
	if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
		logger().Error("upload image: no image_key returned")
		return
	}

	imageKey := *uploadResp.Data.ImageKey

	// Send image message as a reply.
	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	resp, err := b.client.Im.Message.Reply(replyCtx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeImage).
				Content(string(content)).
				Build()).
			Build())
	if err != nil {
		logger().Error("send image failed", "error", err)
		return
	}
	if !resp.Success() {
		logger().Error("send image api error", "code", resp.Code, "msg", resp.Msg)
	}
}
