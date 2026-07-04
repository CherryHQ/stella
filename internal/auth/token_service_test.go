package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type fakeTokenStore struct {
	mu    sync.Mutex
	users map[string]User
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{users: map[string]User{"1": {ID: "1", Email: "alice@example.com"}}}
}

func (s *fakeTokenStore) GetUser(_ context.Context, id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, pgx.ErrNoRows
	}
	return user, nil
}

func TestTokenServiceCreateScopedTokenRequiresActiveUser(t *testing.T) {
	now := time.Now()
	store := newFakeTokenStore()
	svc := NewTokenService(store)
	svc.now = func() time.Time { return now }
	if _, err := svc.CreateScopedToken(context.Background(), "1", "agent-1", "session-1", ""); err == nil {
		t.Fatal("expected inactive user to be rejected")
	}

	store.users["1"] = User{ID: "1", Email: "alice@example.com", IsActive: true}
	tok, err := svc.CreateScopedToken(context.Background(), "1", "agent-1", "session-1", "")
	if err != nil {
		t.Fatalf("CreateScopedToken: %v", err)
	}
	user, claims, err := svc.AuthenticateScoped(context.Background(), tok)
	if err != nil {
		t.Fatalf("AuthenticateScoped: %v", err)
	}
	if user.ID != "1" || claims.AgentID != "agent-1" || claims.SessionID != "session-1" {
		t.Fatalf("user=%+v claims=%+v", user, claims)
	}
}

func TestScopedTokenSecretFromEnv(t *testing.T) {
	t.Setenv(scopedTokenSecretEnv, "stable-secret")
	if got := string(NewTokenService(newFakeTokenStore()).scopedSecret); got != "stable-secret" {
		t.Fatalf("scoped secret = %q", got)
	}
}
