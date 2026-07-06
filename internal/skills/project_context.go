package skills

import "context"

type projectRootContextKey struct{}

func WithProjectRoot(ctx context.Context, projectRoot string) context.Context {
	if projectRoot == "" {
		return ctx
	}
	return context.WithValue(ctx, projectRootContextKey{}, projectRoot)
}

func projectRootFromContext(ctx context.Context, fallback string) string {
	if ctx != nil {
		if projectRoot, ok := ctx.Value(projectRootContextKey{}).(string); ok && projectRoot != "" {
			return projectRoot
		}
	}
	return fallback
}
