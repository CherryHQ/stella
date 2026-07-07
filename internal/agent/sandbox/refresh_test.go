package sandbox

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/auth"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// refreshSession is a fake session whose env can be refreshed in place.
type refreshSession struct {
	pkgsandbox.Session
	env map[string]string
}

func (s *refreshSession) Policy() pkgsandbox.Policy { return pkgsandbox.Policy{Env: s.env} }

func (s *refreshSession) RefreshEnv(updates map[string]string) {
	maps.Copy(s.env, updates)
}

// staticSession is a fake session that cannot refresh its env (no EnvRefresher).
type staticSession struct {
	pkgsandbox.Session
	env map[string]string
}

func (s *staticSession) Policy() pkgsandbox.Policy { return pkgsandbox.Policy{Env: s.env} }

// recordingEnsurer records CreateScopedToken calls and returns a fixed token,
// or err when set. lastUserID captures the user id passed to the last call.
type recordingEnsurer struct {
	token      string
	err        error
	calls      int
	lastUserID string
}

func (e *recordingEnsurer) CreateScopedToken(_ context.Context, userID, _, _, _ string) (string, error) {
	e.calls++
	e.lastUserID = userID
	if e.err != nil {
		return "", e.err
	}
	return e.token, nil
}

func signTokenExpiring(t *testing.T, in time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	tok, err := auth.SignScopedToken([]byte("test-secret"), auth.ScopedTokenClaims{
		UserID:    "u1",
		AgentID:   "a1",
		ExpiresAt: now.Add(in).Unix(),
		IssuedAt:  now.Unix(),
	}, now)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

func refreshConfig(te TokenEnsurer) Config {
	return Config{UserID: "u1", AgentID: "a1", TokenEnsurer: te}
}

func TestRefreshScopedTokenReSignsNearExpiry(t *testing.T) {
	fresh := signTokenExpiring(t, 30*time.Minute) // below the 1h threshold
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": fresh}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}
	secretValues := NewSessionSecretValues()
	secretValues.Set([]string{fresh})
	cfg := refreshConfig(ensurer)
	cfg.SessionSecretValues = secretValues

	RefreshScopedToken(context.Background(), sess, cfg)

	if ensurer.calls != 1 {
		t.Fatalf("expected 1 re-sign, got %d", ensurer.calls)
	}
	if got := sess.env["STELLA_TOKEN"]; got != "stella_scoped_new" {
		t.Fatalf("token not refreshed: %q", got)
	}
	values := secretValues.Values()
	if !slices.Contains(values, fresh) || !slices.Contains(values, "stella_scoped_new") {
		t.Fatalf("session secret values = %#v, want old and refreshed tokens", values)
	}
}

func TestRefreshScopedTokenSkipsWhenFresh(t *testing.T) {
	fresh := signTokenExpiring(t, 5*time.Hour) // well above the 1h threshold
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": fresh}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}

	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	if ensurer.calls != 0 {
		t.Fatalf("expected no re-sign for a fresh token, got %d", ensurer.calls)
	}
	if got := sess.env["STELLA_TOKEN"]; got != fresh {
		t.Fatalf("fresh token should be untouched, got %q", got)
	}
}

func TestRefreshScopedTokenNoOpWithoutRefresher(t *testing.T) {
	fresh := signTokenExpiring(t, 1*time.Minute)
	sess := &staticSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": fresh}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}

	// Must not panic and must not re-sign a token it can't install.
	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	if ensurer.calls != 0 {
		t.Fatalf("expected no re-sign when session can't refresh env, got %d", ensurer.calls)
	}
}

func TestRefreshScopedTokenNoOpWithoutToken(t *testing.T) {
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}

	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	if ensurer.calls != 0 {
		t.Fatalf("expected no re-sign when env carries no token, got %d", ensurer.calls)
	}
}

func TestRefreshScopedTokenReSignsUnparseableToken(t *testing.T) {
	// A token we can't parse is treated as near-expiry and re-signed, so a
	// corrupt token can't pin a runner to a permanently failing credential.
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": "garbage"}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}

	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	if ensurer.calls != 1 {
		t.Fatalf("expected 1 re-sign for an unparseable token, got %d", ensurer.calls)
	}
	if got := sess.env["STELLA_TOKEN"]; got != "stella_scoped_new" {
		t.Fatalf("token not refreshed: %q", got)
	}
}

func TestRefreshScopedTokenGroupPrincipal(t *testing.T) {
	near := signTokenExpiring(t, 10*time.Minute)
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": near}}
	ensurer := &recordingEnsurer{token: "stella_scoped_new"}
	cfg := Config{GroupID: "g1", AgentID: "a1", TokenEnsurer: ensurer}

	RefreshScopedToken(context.Background(), sess, cfg)

	// Group sessions sign against the synthetic "group:<id>" principal, matching
	// buildSandboxEnv's injection.
	if ensurer.lastUserID != "group:g1" {
		t.Fatalf("expected group principal, got %q", ensurer.lastUserID)
	}
}

func TestRefreshScopedTokenKeepsOldTokenOnSignFailure(t *testing.T) {
	near := signTokenExpiring(t, 10*time.Minute)
	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"STELLA_TOKEN": near}}
	ensurer := &recordingEnsurer{err: errors.New("sign failed")}

	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	// A re-sign failure must leave the still-valid old token in place rather than
	// blanking it; the next turn retries.
	if got := sess.env["STELLA_TOKEN"]; got != near {
		t.Fatalf("old token should survive a sign failure, got %q", got)
	}
}
