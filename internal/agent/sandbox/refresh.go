package sandbox

import (
	"context"
	"log/slog"
	"maps"
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

// RefreshSessionEnv reloads the OAuth bundles behind the session's env specs and
// atomically updates the derived sandbox env in place, so a long-lived cached
// runner keeps serving valid OAuth-derived tool credentials as access tokens
// rotate (#722). Each provider bundle is reloaded through the TokenManager,
// which refreshes a token below the min-validity floor (chat timeout + margin)
// and persists it, or consumes one a concurrent replica just wrote.
//
// It refreshes only env vars actually injected from OAuth at session creation
// (recorded in cfg.OAuthEnvBindings), so an explicit vault override of the same
// name — which injectSessionEnv leaves untouched — is never clobbered. It is a
// no-op for group sessions (which must never touch the human OAuth vault, D9),
// sessions whose env cannot be refreshed, or a config without a TokenManager or
// oauth-sourced bindings. A reload/refresh failure for a provider is logged and
// leaves that env var at its previous value — which keeps working until it
// actually expires — rather than clearing it.
func RefreshSessionEnv(ctx context.Context, session pkgsandbox.Session, cfg Config) {
	if session == nil || cfg.GroupID != "" || cfg.TokenManager == nil {
		return
	}
	refresher, ok := session.(pkgsandbox.EnvRefresher)
	if !ok {
		return
	}

	minValidity := oauthMinValidity(cfg)
	// Group bound specs by provider in declaration order. Each provider is
	// staged independently so a scope failure or token error never contributes
	// a partial credential set, while another provider can still refresh.
	providerSpecs := make(map[string][]pkgplugins.SessionEnvSpec)
	var providerOrder []string
	for _, spec := range cfg.SessionEnvSpecs {
		src := string(spec.Source)
		if !strings.HasPrefix(src, "oauth.") || !cfg.OAuthEnvBindings.Has(spec.EnvVar) || spec.OAuthProviderID == "" {
			continue
		}
		if _, exists := providerSpecs[spec.OAuthProviderID]; !exists {
			providerOrder = append(providerOrder, spec.OAuthProviderID)
		}
		providerSpecs[spec.OAuthProviderID] = append(providerSpecs[spec.OAuthProviderID], spec)
	}

	updates := make(map[string]string)
	var rotatedSecrets []string
	for _, providerID := range providerOrder {
		specs := providerSpecs[providerID]
		bundle, err := cfg.TokenManager.GetOAuthToken(ctx, providerID, cfg.UserID, minValidity)
		if err != nil {
			// Preserve the old env: log and skip rather than clear a value
			// that may still work until it truly expires.
			slog.Warn("session env refresh skipped: oauth token unavailable",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"provider", providerID,
				"error", err,
			)
			continue
		}
		providerUpdates, ok := oauthSessionEnvValues(specs, bundle)
		if !ok {
			slog.Debug("session env refresh skipped: OAuth requirements unavailable", "component", "runner_sandbox", "provider", providerID)
			continue
		}
		var providerSecrets []string
		for _, spec := range specs {
			if value, present := providerUpdates[spec.EnvVar]; present && oauthSessionEnvFieldSecret(strings.TrimPrefix(string(spec.Source), "oauth.")) {
				providerSecrets = append(providerSecrets, value)
			}
		}
		maps.Copy(updates, providerUpdates)
		rotatedSecrets = append(rotatedSecrets, providerSecrets...)
	}
	if len(updates) > 0 {
		// Register rotated values before making them executable so an overlapping
		// turn cannot observe a live token before output redaction knows it.
		cfg.SessionSecretValues.Add(rotatedSecrets...)
		refresher.RefreshEnv(updates)
	}
}
