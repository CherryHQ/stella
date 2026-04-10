package feishu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/pkg/channel"
)

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
