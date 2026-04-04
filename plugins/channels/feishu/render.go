package feishu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/agent/runner"
)

// sendImage decodes a base64 image, uploads it to Feishu to obtain an image_key,
// then sends it as an image message in the chat.
func (b *Bot) sendImage(chatID, replyMsgID string, img runner.ImageEvent) {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return
	}

	// Upload image to get image_key.
	uploadResp, err := b.client.Im.Image.Create(b.ctx,
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
	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	resp, err := b.client.Im.Message.Reply(b.ctx,
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

// splitMessage splits a message into chunks that fit within Feishu's message
// length limit. It tries to split at newline boundaries when possible.
func splitMessage(text string) []string {
	if len(text) <= feishuMaxMessageLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= feishuMaxMessageLen {
			chunks = append(chunks, text)
			break
		}

		cutAt := feishuMaxMessageLen
		// Avoid splitting in the middle of a multi-byte UTF-8 character.
		for cutAt > 0 && !utf8.RuneStart(text[cutAt]) {
			cutAt--
		}
		if idx := strings.LastIndex(text[:cutAt], "\n"); idx > 0 {
			cutAt = idx + 1
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}

	return chunks
}
