package host

import (
	"context"

	"github.com/CherryHQ/stella/internal/auth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type pluginAuthStore interface {
	auth.UserStore
	auth.ChannelIdentityStore
}

type authService struct {
	store pluginAuthStore
}

func NewAuthService(store pluginAuthStore) pkgplugins.Auth {
	if store == nil {
		return nil
	}
	return authService{store: store}
}

func (s authService) GetUser(ctx context.Context, userID string) (pkgplugins.UserInfo, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return pkgplugins.UserInfo{}, err
	}
	return pkgplugins.UserInfo{
		ID:               user.ID,
		Username:         user.Email,
		DefaultAgentID:   user.DefaultAgentID,
		NotifyIdentityID: user.NotifyIdentityID,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	}, nil
}

func (s authService) ListUserIdentities(ctx context.Context, userID string) ([]pkgplugins.LinkedIdentity, error) {
	identities, err := s.store.ListChannelIdentitiesByUser(ctx, userID)
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
		})
	}
	return out, nil
}

func (s authService) GetIdentityByPlatform(ctx context.Context, platform, externalID string) (pkgplugins.LinkedIdentity, error) {
	identity, err := s.store.GetChannelIdentityByPlatform(ctx, platform, externalID)
	if err != nil {
		return pkgplugins.LinkedIdentity{}, err
	}
	return pkgplugins.LinkedIdentity{
		ID:         identity.ID,
		UserID:     identity.UserID,
		Platform:   identity.Platform,
		ExternalID: identity.ExternalID,
		Name:       identity.Name,
	}, nil
}
