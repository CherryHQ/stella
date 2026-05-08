package qq

import (
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/tencent-connect/botgo/dto"
)

// sendFinalResponse sends the completed response, splitting into chunks
// if necessary.
func (b *Bot) sendFinalResponse(targetID, msgID, response string, scope messageScope) {
	chunks := channel.SplitMessage(response, qqMaxMessageLen)
	for i, chunk := range chunks {
		// MsgSeq starts at 100 to avoid collisions with stream chunk
		// sequence numbers (which start at 1).
		msg := dto.MessageToCreate{
			Content: chunk,
			MsgType: dto.TextMsg,
			MsgID:   msgID,
			MsgSeq:  uint32(100 + i),
		}

		var err error
		switch scope {
		case scopeC2C:
			_, err = b.api.PostC2CMessage(b.ctx, targetID, msg)
		case scopeGroup:
			_, err = b.api.PostGroupMessage(b.ctx, targetID, msg)
		}

		if err != nil {
			logger().Error("send final response failed", "error", err, "chunk", i)
		}
	}
}

// sendImage is a no-op: QQ's rich media API requires an HTTP/HTTPS URL and
// does not accept raw binary or base64 data. Agent-generated images are
// base64 in-memory with no public URL, so we skip them.
// TODO: support image sending once a file-upload or proxy solution is available.
func (b *Bot) sendImage(_ string, _ string, _ channel.ImageEvent, _ messageScope) {
	logger().Debug("skipping image send: QQ requires HTTP URL for rich media")
}
