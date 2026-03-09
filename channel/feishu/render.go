package feishu

import (
	"strings"
	"unicode/utf8"

	"github.com/vaayne/anna/agent/runner"
)

// sendFinalResponse sends the completed response, splitting into chunks
// if necessary. If we already sent a streaming message, the final update
// was applied in streamResponse, so this only handles the case where
// streaming didn't produce any output.
func (b *Bot) sendFinalResponse(chatID, replyMsgID, response string) {
	chunks := splitMessage(response)
	for _, chunk := range chunks {
		if _, err := b.sendReplyMessage(replyMsgID, chunk); err != nil {
			logger().Error("send final response failed", "error", err, "chat_id", chatID)
		}
	}
}

// sendImage is a no-op: Feishu's image API requires uploading images first
// to get an image_key. Agent-generated images are base64 in-memory.
// TODO: support image sending once image upload is implemented.
func (b *Bot) sendImage(_ string, _ string, _ runner.ImageEvent) {
	logger().Debug("skipping image send: Feishu requires image upload for image_key")
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
