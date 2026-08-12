package skills

import "context"

type projectRootContextKey struct{}

type projectSnapshotContextKey struct{}

func WithProjectSnapshot(ctx context.Context, snapshot *ProjectSnapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, projectSnapshotContextKey{}, snapshot)
}

func projectSnapshotFromContext(ctx context.Context) *ProjectSnapshot {
	if ctx == nil {
		return nil
	}
	snapshot, _ := ctx.Value(projectSnapshotContextKey{}).(*ProjectSnapshot)
	return snapshot
}

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
