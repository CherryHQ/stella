package plugins

import (
	"context"
	"time"
)

// UserInfo is the host-owned user record exposed to plugins.
type UserInfo struct {
	ID               string
	Username         string
	Role             string
	IsActive         bool
	DefaultAgentID   string
	NotifyIdentityID *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// LinkedIdentity is the host-owned linked identity record exposed to plugins.
type LinkedIdentity struct {
	ID         string
	UserID     string
	Platform   string
	ExternalID string
	Name       string
	LinkedAt   time.Time
}

// Auth exposes narrow user and identity lookups without leaking auth internals.
type Auth interface {
	GetUser(ctx context.Context, userID string) (UserInfo, error)
	ListUserIdentities(ctx context.Context, userID string) ([]LinkedIdentity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (LinkedIdentity, error)
}
