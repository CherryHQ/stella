package qq

import (
	"strings"
	"unicode/utf8"

	"github.com/tencent-connect/botgo/dto"
	"github.com/vaayne/anna/agent/runner"
)

// sendFinalResponse sends the completed response, splitting into chunks
// if necessary.
func (b *Bot) sendFinalResponse(targetID, msgID, response string, scope messageScope) {
	chunks := splitMessage(response)
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
func (b *Bot) sendImage(_ string, _ string, _ runner.ImageEvent, _ messageScope) {
	logger().Debug("skipping image send: QQ requires HTTP URL for rich media")
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
