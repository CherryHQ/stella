package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// extractJSONField extracts a string field from a JSON object.
func extractJSONField(raw, field string) string {
	if raw == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// extractJSONInt extracts an integer field from a JSON object.
func extractJSONInt(raw, field string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return 0, false
	}
	v, ok := m[field]
	if !ok {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	return n, true
}

// parseTextContent extracts text from Feishu's JSON content format.
func parseTextContent(raw string) string {
	if raw == "" {
		return ""
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return raw
	}
	return content.Text
}

// parseAudioContent returns descriptive text for an audio message.
func parseAudioContent(raw string) string {
	duration, ok := extractJSONInt(raw, "duration")
	if ok && duration > 0 {
		return fmt.Sprintf("[Audio message, duration: %ds]", duration/1000)
	}
	return "[Audio message]"
}

// parseVideoContent returns descriptive text for a video message.
func parseVideoContent(raw string) string {
	duration, ok := extractJSONInt(raw, "duration")
	if ok && duration > 0 {
		return fmt.Sprintf("[Video message, duration: %ds]", duration/1000)
	}
	return "[Video message]"
}

// parseFileContent returns descriptive text for a file message.
func parseFileContent(raw string) string {
	name := extractJSONField(raw, "file_name")
	if name != "" {
		return fmt.Sprintf("[File: %s]", name)
	}
	return "[File]"
}

// parseStickerContent returns descriptive text for a sticker message.
func parseStickerContent(_ string) string {
	return "[Sticker]"
}

// parseLocationContent returns descriptive text for a location message.
func parseLocationContent(raw string) string {
	name := extractJSONField(raw, "name")
	lat := extractJSONField(raw, "latitude")
	lng := extractJSONField(raw, "longitude")
	if name != "" && lat != "" && lng != "" {
		return fmt.Sprintf("[Location: %s (%s, %s)]", name, lat, lng)
	}
	if name != "" {
		return fmt.Sprintf("[Location: %s]", name)
	}
	return "[Location]"
}

// parseShareChatContent returns descriptive text for a shared chat message.
func parseShareChatContent(raw string) string {
	chatID := extractJSONField(raw, "chat_id")
	if chatID != "" {
		return fmt.Sprintf("[Shared chat: %s]", chatID)
	}
	return "[Shared chat]"
}

// parseShareUserContent returns descriptive text for a shared user message.
func parseShareUserContent(raw string) string {
	userID := extractJSONField(raw, "user_id")
	if userID != "" {
		return fmt.Sprintf("[Shared user: %s]", userID)
	}
	return "[Shared user]"
}

// parseMergeForwardContent presents the child messages returned by Message.Get.
// The receive event contains only Feishu's fixed merge-forward placeholder.
func parseMergeForwardContent(upperMessageID string, messages []*larkim.Message) string {
	var parts []string
	for _, message := range messages {
		if message == nil || derefStr(message.UpperMessageId) != upperMessageID {
			continue
		}
		if text := parseForwardedMessageContent(message); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "[Forwarded messages]"
	}
	return "[Forwarded messages]\n\n" + strings.Join(parts, "\n\n")
}

func parseForwardedMessageContent(message *larkim.Message) string {
	msgType := derefStr(message.MsgType)
	rawContent := ""
	if message.Body != nil {
		rawContent = derefStr(message.Body.Content)
	}

	switch msgType {
	case "text":
		return parseTextContent(rawContent)
	case "post":
		text, _ := parsePostBlocks(rawContent)
		return text
	case "audio":
		return parseAudioContent(rawContent)
	case "video", "media":
		return parseVideoContent(rawContent)
	case "file":
		return parseFileContent(rawContent)
	case "sticker":
		return parseStickerContent(rawContent)
	case "location":
		return parseLocationContent(rawContent)
	case "share_chat":
		return parseShareChatContent(rawContent)
	case "share_user":
		return parseShareUserContent(rawContent)
	case "image":
		return "[Image]"
	case "interactive":
		return "[Interactive card]"
	default:
		return fmt.Sprintf("[Unsupported forwarded message type: %s]", msgType)
	}
}

// postNode represents a single element inside a Feishu "post" rich-text message.
type postNode struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	UserName string `json:"user_name,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
}

// parsePostBlocks parses Feishu post (rich text) JSON content and returns the
// concatenated plain text along with the (deduplicated) image keys found across
// all paragraphs. Received post content is the flat
// {"title":"...","content":[[{node}]]} shape; the locale-wrapped
// {"zh_cn":{...}} form is unwrapped first as a defensive fallback. Supported
// nodes: text, a (link), at (mention), img; media renders as a placeholder and
// other tags are ignored.
func parsePostBlocks(raw string) (text string, imageKeys []string) {
	if raw == "" {
		return "", nil
	}

	var post struct {
		Title   string       `json:"title"`
		Content [][]postNode `json:"content"`
	}
	if err := json.Unmarshal(unwrapPostLocale(raw), &post); err != nil {
		return raw, nil
	}

	seen := make(map[string]bool)
	var textParts []string
	if post.Title != "" {
		textParts = append(textParts, post.Title)
	}
	for _, paragraph := range post.Content {
		var line string
		for _, node := range paragraph {
			switch node.Tag {
			case "text":
				line += node.Text
			case "a":
				line += postLinkText(node)
			case "at":
				if node.UserName != "" {
					line += "@" + node.UserName
				}
			case "media":
				line += "[Video]"
			case "img":
				if node.ImageKey != "" && !seen[node.ImageKey] {
					seen[node.ImageKey] = true
					imageKeys = append(imageKeys, node.ImageKey)
				}
			}
		}
		if line != "" {
			textParts = append(textParts, line)
		}
	}

	return strings.Join(textParts, "\n"), imageKeys
}

// postLinkText renders an "a" (link) node, keeping both display text and URL
// when they differ so the model sees the destination.
func postLinkText(node postNode) string {
	switch {
	case node.Text != "" && node.Href != "" && node.Text != node.Href:
		return node.Text + " (" + node.Href + ")"
	case node.Text != "":
		return node.Text
	default:
		return node.Href
	}
}

// unwrapPostLocale returns the inner content of a locale-wrapped post
// ({"zh_cn":{...}}) when present, otherwise the original bytes. Received post
// events are already flat, so this only guards against the send-shaped form.
func unwrapPostLocale(raw string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return []byte(raw)
	}
	for _, locale := range []string{"zh_cn", "en_us", "ja_jp"} {
		if v, ok := m[locale]; ok {
			return v
		}
	}
	return []byte(raw)
}
