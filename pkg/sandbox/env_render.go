package sandbox

import (
	"context"
	"errors"
	"maps"
)

// RenderEnv applies a raw session's fixed process coordinate system to one
// logical turn environment. The caller owns generation selection; this helper
// never recreates or reselects a session.
func RenderEnv(ctx context.Context, session Session, logicalEnv map[string]string) (map[string]string, error) {
	if session == nil {
		return nil, errors.New("sandbox: active session is required")
	}
	if renderer, ok := session.(EnvRenderer); ok {
		return renderer.RenderEnv(ctx, logicalEnv)
	}
	return maps.Clone(logicalEnv), nil
}
