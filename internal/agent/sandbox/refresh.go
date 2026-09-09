package sandbox

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// defaultChatTurnTimeout mirrors the runner's own defaultChatTimeout. It is the
// fallback turn budget when Config.ChatTimeout is unset, so OAuth min-validity
// still covers a full-length turn.
const defaultChatTurnTimeout = 30 * time.Minute

// oauthValiditySafetyMargin pads the OAuth min-validity above the turn budget so
// a token refreshed at turn start comfortably outlives the turn plus the trailing
// egress/tool calls, absorbing clock skew across replicas.
const oauthValiditySafetyMargin = 5 * time.Minute

// oauthMinValidity is the remaining lifetime an OAuth access token must have at
// turn start to be trusted for the whole turn: the configured chat timeout plus
// a safety margin (#722). It replaces the old fixed 10-minute window, which
// could not guarantee a token outlived a 30-minute turn.
func oauthMinValidity(cfg Config) time.Duration {
	timeout := cfg.ChatTimeout
	if timeout <= 0 {
		timeout = defaultChatTurnTimeout
	}
	return timeout + oauthValiditySafetyMargin
}

// SessionEnvRefreshResult is the per-turn authorization result for OAuth
// session environment. Ready covers required bindings; optional packages with
// a failed live binding are still listed in UnavailablePluginIDs so the caller
// can remove them before exposing model-facing resources.
type SessionEnvRefreshResult struct {
	Ready                bool
	UnavailablePluginIDs []string
	UnavailableProviders []string
}

// PluginUnavailable reports whether this turn lacks a usable OAuth binding for
// pluginID.
func (r SessionEnvRefreshResult) PluginUnavailable(pluginID string) bool {
	return slices.Contains(r.UnavailablePluginIDs, pluginID)
}

// RefreshSessionEnv reloads the OAuth bundles behind the session's env specs and
// atomically updates the derived sandbox env in place, so a long-lived cached
// runner keeps serving valid OAuth-derived tool credentials as access tokens
// rotate (#722). Each provider bundle is reloaded through the TokenManager,
// which refreshes a token below the min-validity floor (chat timeout + margin)
// and persists it, or consumes one a concurrent replica just wrote.
//
// It refreshes only env vars actually injected from OAuth at session creation
// (recorded in cfg.OAuthEnvBindings), so an explicit vault override of the same
// name — which injectSessionEnv leaves untouched — is never clobbered. Group
// sessions, sessions whose env cannot be refreshed, and configs without a
// TokenManager return unavailable package IDs without touching the human OAuth
// vault or session env. A reload/refresh failure for a provider is logged and
// leaves the old env untouched for cleanup, while the result marks affected
// packages unavailable so callers cannot dispatch the stale credential.
func RefreshSessionEnv(ctx context.Context, session pkgsandbox.Session, cfg Config) SessionEnvRefreshResult {
	result := SessionEnvRefreshResult{Ready: true}
	policyEnv := map[string]string(nil)
	if session != nil {
		policyEnv = session.Policy().Env
	}

	// Group specs by provider and package. A required unbound spec is
	// still checked when no current env value satisfies it. Unbound values that
	// are present came from an explicit vault override and must not be replaced.
	providerPackageSpecs := make(map[string]map[string][]pkgplugins.SessionEnvSpec)
	for _, spec := range cfg.SessionEnvSpecs {
		src := string(spec.Source)
		if !strings.HasPrefix(src, "oauth.") || spec.OAuthProviderID == "" {
			continue
		}
		if !cfg.OAuthEnvBindings.Has(spec.EnvVar) && policyEnv[spec.EnvVar] != "" {
			continue
		}
		packages, exists := providerPackageSpecs[spec.OAuthProviderID]
		if !exists {
			packages = make(map[string][]pkgplugins.SessionEnvSpec)
			providerPackageSpecs[spec.OAuthProviderID] = packages
		}
		packages[spec.PluginID] = append(packages[spec.PluginID], spec)
	}

	if len(providerPackageSpecs) == 0 {
		return result
	}
	markUnavailable := func(providerID string, specs []pkgplugins.SessionEnvSpec) {
		if !slices.Contains(result.UnavailableProviders, providerID) {
			result.UnavailableProviders = append(result.UnavailableProviders, providerID)
		}
		for _, spec := range specs {
			bound := cfg.OAuthEnvBindings.Has(spec.EnvVar)
			if !bound && policyEnv[spec.EnvVar] != "" {
				continue
			}
			if spec.Required {
				result.Ready = false
			}
			if spec.PluginID != "" && !result.PluginUnavailable(spec.PluginID) {
				result.UnavailablePluginIDs = append(result.UnavailablePluginIDs, spec.PluginID)
			}
		}
	}
	providerIDs := slices.Sorted(maps.Keys(providerPackageSpecs))
	markProviderUnavailable := func(providerID string) {
		for _, packageID := range slices.Sorted(maps.Keys(providerPackageSpecs[providerID])) {
			markUnavailable(providerID, providerPackageSpecs[providerID][packageID])
		}
	}

	if session == nil || cfg.GroupID != "" || cfg.TokenManager == nil {
		for _, providerID := range providerIDs {
			markProviderUnavailable(providerID)
		}
		return result
	}
	refresher, ok := session.(pkgsandbox.EnvRefresher)
	if !ok {
		for _, providerID := range providerIDs {
			markProviderUnavailable(providerID)
		}
		return result
	}

	minValidity := oauthMinValidity(cfg)
	updates := make(map[string]string)
	var rotatedSecrets []string
	for _, providerID := range providerIDs {
		bundle, err := cfg.TokenManager.GetOAuthToken(ctx, providerID, cfg.UserID, minValidity)
		if err != nil {
			// GetOAuthToken was asked for the full turn validity floor. On error,
			// leave the old value only for cleanup; the result removes affected
			// packages from the turn view instead of reusing that value.
			markProviderUnavailable(providerID)
			slog.Warn("session env refresh skipped: oauth token unavailable",
				"component", "runner_sandbox", "user_id", cfg.UserID,
				"provider", providerID, "error", err)
			continue
		}
		for _, packageID := range slices.Sorted(maps.Keys(providerPackageSpecs[providerID])) {
			packageSpecs := providerPackageSpecs[providerID][packageID]
			providerUpdates, valuesOK := oauthSessionEnvValues(packageSpecs, bundle)
			if !valuesOK {
				markUnavailable(providerID, packageSpecs)
				slog.Debug("session env refresh skipped: OAuth requirements unavailable", "component", "runner_sandbox", "provider", providerID, "plugin", packageID)
				continue
			}
			for _, spec := range packageSpecs {
				bound := cfg.OAuthEnvBindings.Has(spec.EnvVar)
				if !bound && !spec.Required {
					continue
				}
				if !bound && policyEnv[spec.EnvVar] != "" {
					continue
				}
				if !bound || providerUpdates[spec.EnvVar] == "" {
					// This session never received the required binding, or a bound
					// available token belongs to the next rebuilt session, rather
					// than being silently introduced into this runner. A bound
					// optional value that disappeared is also unavailable: keeping
					// its old env would reuse a revoked credential.
					markUnavailable(providerID, packageSpecs)
					valuesOK = false
					break
				}
			}
			if !valuesOK {
				continue
			}
			for _, spec := range packageSpecs {
				if !cfg.OAuthEnvBindings.Has(spec.EnvVar) {
					continue
				}
				value, present := providerUpdates[spec.EnvVar]
				if !present {
					continue
				}
				updates[spec.EnvVar] = value
				if oauthSessionEnvFieldSecret(strings.TrimPrefix(string(spec.Source), "oauth.")) {
					rotatedSecrets = append(rotatedSecrets, value)
				}
			}
		}
	}
	if len(updates) > 0 {
		// Register rotated values before making them executable so an overlapping
		// turn cannot observe a live token before output redaction knows it.
		cfg.SessionSecretValues.Add(rotatedSecrets...)
		refresher.RefreshEnv(updates)
	}
	return result
}
