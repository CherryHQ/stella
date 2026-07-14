package sandbox

import (
	"context"
	"log/slog"
	"strings"
	"time"

	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/pkg/auth"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// scopedTokenRefreshThreshold is the remaining-TTL floor below which the sandbox
// STELLA_TOKEN is re-signed. It must stay comfortably above the longest single
// chat turn (defaultChatTimeout, 30m) so a token refreshed at turn start outlives
// the turn, and well under the token's own 6h TTL so refreshes are rare.
const scopedTokenRefreshThreshold = time.Hour

// RefreshScopedToken re-signs the sandbox STELLA_TOKEN and updates the session
// env in place when the current token is near expiry, so long-lived cached
// runners never serve an expired token (#490). It is a no-op when token
// injection is disabled, the session carries no token or can't refresh its env,
// or the token still has ample TTL — the common case, costing one cheap HMAC
// parse per turn. A re-sign failure is logged and left to the next turn; the
// stale token keeps working until it actually expires.
func RefreshScopedToken(ctx context.Context, session pkgsandbox.Session, cfg Config) {
	if session == nil || !shouldInjectScopedToken(cfg) {
		return
	}
	refresher, ok := session.(pkgsandbox.EnvRefresher)
	if !ok {
		return
	}
	current := session.Policy().Env["STELLA_TOKEN"]
	if current == "" {
		return
	}
	if claims, err := auth.ParseScopedTokenUnverified(current); err == nil {
		if time.Until(time.Unix(claims.ExpiresAt, 0)) > scopedTokenRefreshThreshold {
			return
		}
	}

	tokenUserID := cfg.UserID
	if cfg.GroupID != "" {
		tokenUserID = "group:" + cfg.GroupID
	}
	tok, err := cfg.TokenEnsurer.CreateScopedToken(ctx, tokenUserID, cfg.AgentID, cfg.SessionID, cfg.ProjectID)
	if err != nil {
		slog.Warn("scoped token refresh skipped",
			"component", "runner_sandbox",
			"user_id", cfg.UserID,
			"agent_id", cfg.AgentID,
			"error", err,
		)
		return
	}
	refresher.RefreshEnv(map[string]string{"STELLA_TOKEN": tok})
}

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
// It is a no-op for group sessions (which must never touch the human OAuth
// vault, D9), sessions whose env cannot be refreshed, or a config without a
// TokenManager or oauth-sourced specs. A reload/refresh failure for a provider
// is logged and leaves that env var at its previous value — which keeps working
// until it actually expires — rather than clearing it. Static and STELLA_TOKEN
// values are untouched here; the latter has its own refresh path.
func RefreshSessionEnv(ctx context.Context, session pkgsandbox.Session, cfg Config) {
	if session == nil || cfg.GroupID != "" || cfg.TokenManager == nil {
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
	for _, spec := range cfg.SessionEnvSpecs {
		src := string(spec.Source)
		if !strings.HasPrefix(src, "oauth.") {
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
		if value, known := oauthBundleField(bundle, field); known && value != "" {
			updates[spec.EnvVar] = value
		}
	}
	if len(updates) > 0 {
		refresher.RefreshEnv(updates)
	}
}
