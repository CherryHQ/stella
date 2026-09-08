package sandbox

import (
	"context"
	"errors"
	"strings"

	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// PrepareOAuthPackages checks every package's complete OAuth declaration before
// a runner is built. Provider fetches are shared by provider ID, while the
// scope and binding decision remains package-scoped.
func PrepareOAuthPackages(ctx context.Context, cfg Config, requirements []pkgplugins.PluginPackageRequirement) pkgplugins.PluginPreparationResult {
	result := pkgplugins.PluginPreparationResult{Packages: make([]pkgplugins.PluginPackageStatus, 0, len(requirements))}
	if len(requirements) == 0 {
		return result
	}

	tokens := make(map[string]oauthReadinessToken)
	for _, requirement := range requirements {
		status := pkgplugins.PluginPackageStatus{PluginID: requirement.PluginID, Ready: true}
		if requirement.UnavailableReason != "" {
			status.Ready = false
			status.Reason = requirement.UnavailableReason
			result.Packages = append(result.Packages, status)
			continue
		}
		if len(requirement.OAuth) == 0 {
			result.Packages = append(result.Packages, status)
			continue
		}
		if cfg.GroupID != "" {
			status.Ready = false
			status.Reason = "OAuth is unavailable in group sessions"
			result.Packages = append(result.Packages, status)
			continue
		}
		for _, oauthRequirement := range requirement.OAuth {
			token, ok := tokens[oauthRequirement.Provider]
			if !ok {
				token = loadOAuthReadinessToken(ctx, cfg, oauthRequirement.Provider)
				tokens[oauthRequirement.Provider] = token
			}
			if token.err != nil || token.bundle == nil {
				status.Ready = false
				status.Reason = "OAuth binding is unavailable"
				break
			}
			if missing, known := connections.CheckRequiredScopes(oauthRequirement.Scopes, token.bundle.GrantedScope); !known || len(missing) > 0 {
				status.Ready = false
				status.Reason = "required OAuth scopes are unavailable"
				break
			}
			for _, binding := range oauthRequirement.Bindings {
				if binding.Connection != "" {
					status.Ready = false
					status.Reason = "OAuth connection binding is unavailable"
					break
				}
				value, known := oauthBundleField(token.bundle, binding.Credential)
				if !known || strings.TrimSpace(value) == "" {
					status.Ready = false
					status.Reason = "required OAuth binding is unavailable"
					break
				}
			}
			if !status.Ready {
				break
			}
		}
		result.Packages = append(result.Packages, status)
	}
	return result
}

type oauthReadinessToken struct {
	bundle *oauth.OAuthBundle
	err    error
}

var errOAuthUnavailable = errors.New("OAuth binding unavailable")

func loadOAuthReadinessToken(ctx context.Context, cfg Config, provider string) oauthReadinessToken {
	if cfg.TokenManager == nil || cfg.UserID == "" {
		return oauthReadinessToken{err: errOAuthUnavailable}
	}
	bundle, err := cfg.TokenManager.GetOAuthToken(ctx, provider, cfg.UserID, oauthMinValidity(cfg))
	return oauthReadinessToken{bundle: bundle, err: err}
}
