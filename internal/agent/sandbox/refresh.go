package sandbox

import (
	"context"
	"log/slog"
	"time"

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
	cfg.SessionSecretValues.Add(tok)
}
