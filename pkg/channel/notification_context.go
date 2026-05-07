package channel

import "context"

type notificationAgentIDKey struct{}

// WithNotificationAgentID annotates tool execution contexts with the agent
// that produced a notification.
func WithNotificationAgentID(ctx context.Context, agentID string) context.Context {
	if agentID == "" {
		return ctx
	}
	return context.WithValue(ctx, notificationAgentIDKey{}, agentID)
}

// NotificationAgentIDFromContext returns the agent ID associated with a
// notification-producing tool call, if present.
func NotificationAgentIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	agentID, _ := ctx.Value(notificationAgentIDKey{}).(string)
	return agentID
}

// NotificationReplyContext carries thread-reply targeting info so the notify
// tool can reply in-thread without the agent needing to know message IDs.
type NotificationReplyContext struct {
	ChatID    string
	MessageID string
}

type notificationReplyKey struct{}

// WithNotificationReply annotates the context with reply targeting info.
func WithNotificationReply(ctx context.Context, rc NotificationReplyContext) context.Context {
	return context.WithValue(ctx, notificationReplyKey{}, rc)
}

// NotificationReplyFromContext returns the reply context if present.
func NotificationReplyFromContext(ctx context.Context) (NotificationReplyContext, bool) {
	if ctx == nil {
		return NotificationReplyContext{}, false
	}
	rc, ok := ctx.Value(notificationReplyKey{}).(NotificationReplyContext)
	return rc, ok
}
