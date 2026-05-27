package config

import (
	"context"

	"github.com/CherryHQ/stella/internal/orgctx"
)

func WithOrgID(ctx context.Context, orgID string) context.Context {
	return orgctx.WithOrgID(ctx, orgID)
}

func OrgIDFromContext(ctx context.Context) string {
	return orgctx.OrgIDFromContext(ctx)
}
