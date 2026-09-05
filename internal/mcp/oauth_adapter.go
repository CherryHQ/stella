package mcp

import (
	"context"
	"time"

	"golang.org/x/oauth2"

	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"
)

const (
	oauthFlowTTL         = 10 * time.Minute
	oauthExchangeTimeout = 30 * time.Second
	oauthMinValidity     = 60 * time.Second
)

func oauthBundleRef(reg Registration, owner CredentialOwner) credoauth.BundleRef {
	return credoauth.BundleRef{
		ProviderKey: "mcp:" + reg.ID,
		Owner:       credoauth.CredentialOwner{Scope: owner.Scope, UserID: owner.UserID, AgentID: owner.AgentID},
		Name:        oauthBundleName(reg.ID),
	}
}

func (s *Service) oauthProviderConfig(ctx context.Context, reg Registration, bundle *credoauth.OAuthBundle) credoauth.DynamicProviderConfig {
	cfg := credoauth.DynamicProviderConfig{ProviderKey: "mcp:" + reg.ID, HTTPClient: oauthHTTPClient(s.endpoints)}
	if bundle != nil {
		cfg.ClientID = bundle.ClientID
		cfg.TokenURL = bundle.TokenEndpoint
		cfg.AuthStyle = oauth2.AuthStyle(bundle.AuthStyle)
		cfg.Resource = bundle.Resource
	}
	cfg.ClientSecret = s.oauthClientSecret(ctx, reg)
	return cfg
}

func (s *Service) oauthClientSecret(ctx context.Context, reg Registration) string {
	if s.vault == nil {
		return ""
	}
	secret, _ := s.vault.GetScoped(ctx, reg.Scope, reg.UserID, reg.AgentID, oauthClientSecretName(reg.ID))
	return secret
}
