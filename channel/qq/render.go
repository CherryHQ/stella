package qq

import (
	"strings"

	"github.com/tencent-connect/botgo/dto"
)

// sendFinalResponse sends the completed response, splitting into chunks
// if necessary. It sends a final stream-done chunk first, then the full text.
func (b *Bot) sendFinalResponse(targetID, msgID, response string, scope messageScope) {
	chunks := splitMessage(response)
	for i, chunk := range chunks {
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

// splitMessage splits a message into chunks that fit within QQ's message
// length limit. It tries to split at newline boundaries when possible.
func splitMessage(text string) []string {
	if len(text) <= qqMaxMessageLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= qqMaxMessageLen {
			chunks = append(chunks, text)
			break
		}

		cutAt := qqMaxMessageLen
		if idx := strings.LastIndex(text[:cutAt], "\n"); idx > 0 {
			cutAt = idx + 1
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}

	return chunks
}
