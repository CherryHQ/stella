package sandbox

import (
	"context"
	"maps"
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

// recordingEnsurer records CreateScopedToken calls and returns a fixed token.
type recordingEnsurer struct {
	token string
	calls int
}

func (e *recordingEnsurer) EnsureAutoToken(context.Context, string) error { return nil }

func (e *recordingEnsurer) CreateScopedToken(context.Context, string, string, string, string) (string, error) {
	e.calls++
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

	RefreshScopedToken(context.Background(), sess, refreshConfig(ensurer))

	if ensurer.calls != 1 {
		t.Fatalf("expected 1 re-sign, got %d", ensurer.calls)
	}
	if got := sess.env["STELLA_TOKEN"]; got != "stella_scoped_new" {
		t.Fatalf("token not refreshed: %q", got)
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
