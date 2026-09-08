package sandbox

import (
	"context"
	"maps"
)

type turnEnvKey struct{}

// WithTurnEnv attaches the complete logical process environment prepared for
// one admitted turn. Sandbox tools render it into backend-specific coordinates
// before using EnvReplace; the retained session policy remains unchanged
// between turns.
func WithTurnEnv(ctx context.Context, env map[string]string) context.Context {
	return context.WithValue(ctx, turnEnvKey{}, maps.Clone(env))
}

// TurnEnv returns the complete environment for the current turn, if one was
// prepared. The returned map is a defensive copy.
func TurnEnv(ctx context.Context) (map[string]string, bool) {
	env, ok := ctx.Value(turnEnvKey{}).(map[string]string)
	if !ok || env == nil {
		return nil, false
	}
	return maps.Clone(env), true
}
