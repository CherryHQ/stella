package plugins

import (
	"context"
	"time"
)

// UserInfo is the host-owned user record exposed to plugins.
type UserInfo struct {
	ID               int64
	Username         string
	Role             string
	IsActive         bool
	DefaultAgentID   string
	NotifyIdentityID *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// LinkedIdentity is the host-owned linked identity record exposed to plugins.
type LinkedIdentity struct {
	ID         int64
	UserID     int64
	Platform   string
	ExternalID string
	Name       string
	LinkedAt   time.Time
}

// Auth exposes narrow user and identity lookups without leaking auth internals.
type Auth interface {
	GetUser(ctx context.Context, userID int64) (UserInfo, error)
	ListUserIdentities(ctx context.Context, userID int64) ([]LinkedIdentity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (LinkedIdentity, error)
}
