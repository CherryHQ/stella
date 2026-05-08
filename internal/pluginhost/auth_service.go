package pluginhost

import (
	"context"

	"github.com/CherryHQ/stella/internal/auth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type authService struct {
	store auth.AuthStore
}

func NewAuthService(store auth.AuthStore) pkgplugins.Auth {
	if store == nil {
		return nil
	}
	return authService{store: store}
}

func (s authService) GetUser(ctx context.Context, userID int64) (pkgplugins.UserInfo, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return pkgplugins.UserInfo{}, err
	}
	return pkgplugins.UserInfo{
		ID:               user.ID,
		Username:         user.Username,
		Role:             user.Role,
		IsActive:         user.IsActive,
		DefaultAgentID:   user.DefaultAgentID,
		NotifyIdentityID: user.NotifyIdentityID,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	}, nil
}

func (s authService) ListUserIdentities(ctx context.Context, userID int64) ([]pkgplugins.LinkedIdentity, error) {
	identities, err := s.store.ListIdentitiesByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.LinkedIdentity, 0, len(identities))
	for _, identity := range identities {
		out = append(out, pkgplugins.LinkedIdentity{
			ID:         identity.ID,
			UserID:     identity.UserID,
			Platform:   identity.Platform,
			ExternalID: identity.ExternalID,
			Name:       identity.Name,
			LinkedAt:   identity.LinkedAt,
		})
	}
	return out, nil
}

func (s authService) GetIdentityByPlatform(ctx context.Context, platform, externalID string) (pkgplugins.LinkedIdentity, error) {
	identity, err := s.store.GetIdentityByPlatform(ctx, platform, externalID)
	if err != nil {
		return pkgplugins.LinkedIdentity{}, err
	}
	return pkgplugins.LinkedIdentity{
		ID:         identity.ID,
		UserID:     identity.UserID,
		Platform:   identity.Platform,
		ExternalID: identity.ExternalID,
		Name:       identity.Name,
		LinkedAt:   identity.LinkedAt,
	}, nil
}
