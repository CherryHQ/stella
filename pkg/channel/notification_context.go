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
