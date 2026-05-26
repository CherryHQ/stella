package config

import "context"

type ctxKey string

const orgIDKey ctxKey = "orgID"

func WithOrgID(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, orgIDKey, orgID)
}

func OrgIDFromContext(ctx context.Context) string {
	orgID, _ := ctx.Value(orgIDKey).(string)
	return orgID
}
