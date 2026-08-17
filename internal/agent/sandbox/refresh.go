package sandbox

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agentrun"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
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
	if err := agentrun.Check(ctx); err != nil {
		return
	}
	refresher, ok := session.(pkgsandbox.EnvRefresher)
	if !ok {
		return
	}

	minValidity := oauthMinValidity(cfg)
	// bundles caches one resolution per provider so multiple env vars sourced
	// from the same provider trigger a single reload/refresh. A nil entry records
	// a resolution that failed, so we don't retry it within one turn.
	bundles := make(map[string]*oauth.OAuthBundle)
	updates := make(map[string]string)
	var rotatedSecrets []string
	for _, spec := range cfg.SessionEnvSpecs {
		src := string(spec.Source)
		if !strings.HasPrefix(src, "oauth.") {
			continue
		}
		// Only vars injected from OAuth at creation are refreshable; anything the
		// vault explicitly provided (or that was never connected) is left alone.
		if !cfg.OAuthEnvBindings.Has(spec.EnvVar) {
			continue
		}
		providerID := spec.OAuthProviderID
		if providerID == "" {
			continue
		}
		bundle, seen := bundles[providerID]
		if !seen {
			var err error
			bundle, err = cfg.TokenManager.GetOAuthToken(ctx, providerID, cfg.UserID, minValidity)
			if err != nil {
				// Preserve the old env: log and skip rather than clear a value
				// that may still work until it truly expires.
				slog.Warn("session env refresh skipped: oauth token unavailable",
					"component", "runner_sandbox",
					"user_id", cfg.UserID,
					"provider", providerID,
					"env_var", spec.EnvVar,
					"error", err,
				)
				bundle = nil
			}
			bundles[providerID] = bundle
		}
		if bundle == nil {
			continue
		}
		field := strings.TrimPrefix(src, "oauth.")
		value, known := oauthBundleField(bundle, field)
		if known && value != "" {
			updates[spec.EnvVar] = value
			if oauthSessionEnvFieldSecret(field) {
				rotatedSecrets = append(rotatedSecrets, value)
			}
		}
	}
	if len(updates) > 0 {
		if err := agentrun.Check(ctx); err != nil {
			return
		}
		// Register rotated values before making them executable so an overlapping
		// turn cannot observe a live token before output redaction knows it.
		cfg.SessionSecretValues.Add(rotatedSecrets...)
		refresher.RefreshEnv(updates)
	}
}
