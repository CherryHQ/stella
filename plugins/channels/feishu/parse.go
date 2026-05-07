package feishu

import (
	"encoding/json"
	"fmt"
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

// parseMergeForwardContent returns descriptive text for a merge-forwarded message.
func parseMergeForwardContent(_ string) string {
	return "[Forwarded messages]"
}
