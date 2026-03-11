package lcm

import "context"

type contextKey string

const sessionIDKey contextKey = "lcm_session_id"

// WithSessionID attaches a session ID to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext extracts the session ID from context.
func SessionIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(sessionIDKey).(string)
	return s
}
