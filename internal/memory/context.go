package memory

import "context"

type contextKey string

const (
	sessionIDKey contextKey = "memory_session_id"
	userIDKey    contextKey = "memory_user_id"
	agentIDKey   contextKey = "memory_agent_id"
)

// WithSessionID attaches a session ID to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext extracts the session ID from context.
func SessionIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(sessionIDKey).(string)
	return s
}

// WithUserID attaches a user ID to the context.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext extracts the user ID from context.
func UserIDFromContext(ctx context.Context) int64 {
	id, _ := ctx.Value(userIDKey).(int64)
	return id
}

// WithAgentID attaches an agent ID to the context.
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, agentIDKey, agentID)
}

// AgentIDFromContext extracts the agent ID from context.
func AgentIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(agentIDKey).(string)
	return s
}
