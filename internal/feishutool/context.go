package feishutool

import "context"

type contextKey string

const (
	openIDKey    contextKey = "feishu_open_id"
	chatIDKey    contextKey = "feishu_chat_id"
	messageIDKey contextKey = "feishu_message_id"
)

// WithOpenID attaches a Feishu open_id to the context.
func WithOpenID(ctx context.Context, openID string) context.Context {
	return context.WithValue(ctx, openIDKey, openID)
}

// OpenIDFromContext extracts the Feishu open_id from context.
func OpenIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(openIDKey).(string)
	return s
}

// WithChatID attaches a Feishu chat_id to the context.
func WithChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, chatIDKey, chatID)
}

// ChatIDFromContext extracts the Feishu chat_id from context.
func ChatIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(chatIDKey).(string)
	return s
}

// WithMessageID attaches a Feishu message_id to the context.
func WithMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIDKey, messageID)
}

// MessageIDFromContext extracts the Feishu message_id from context.
func MessageIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(messageIDKey).(string)
	return s
}
