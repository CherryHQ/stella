package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/vault"
	pkgauth "github.com/CherryHQ/stella/pkg/auth"
)

const (
	// StellaTokenName is re-exported from the vault package for callers that only
	// import auth.
	StellaTokenName      = vault.StellaTokenName
	scopedTokenSecretEnv = "STELLA_SCOPED_TOKEN_SECRET"
)

type tokenStore interface {
	GetUser(ctx context.Context, id string) (User, error)
}

// VaultWriter writes internal plaintext secrets to the per-user vault.
type VaultWriter interface {
	SetReserved(ctx context.Context, userID string, name string, plaintext string) error
}

// TokenService owns API token lifecycle and authentication.
type TokenService struct {
	store        tokenStore
	vault        VaultWriter
	now          func() time.Time
	scopedSecret []byte
}

// NewTokenService creates a token service backed by auth persistence and vault writes.
func NewTokenService(store tokenStore, vault VaultWriter) *TokenService {
	return &TokenService{store: store, vault: vault, now: time.Now, scopedSecret: scopedTokenSecret()}
}

// CreateScopedToken signs a short-lived sandbox token bound to one user-agent session.
// For group sessions, userID is a group principal ("group:{group_id}") and the
// token is signed without an auth user lookup — the group principal is not a
// human user, so GetUser would fail.
func (s *TokenService) CreateScopedToken(ctx context.Context, userID, agentID, sessionID, projectID string) (string, error) {
	if !strings.HasPrefix(userID, "group:") {
		user, err := s.store.GetUser(ctx, userID)
		if err != nil {
			return "", fmt.Errorf("token service: get user %s: %w", userID, err)
		}
		if !user.IsActive {
			return "", fmt.Errorf("token service: user %s is inactive", userID)
		}
	}
	return pkgauth.SignScopedToken(s.scopedSecret, pkgauth.ScopedTokenClaims{
		UserID:    userID,
		AgentID:   agentID,
		SessionID: sessionID,
		ProjectID: projectID,
	}, s.now())
}

// AuthenticateScoped returns the active user and scoped claims identified by rawToken.
func (s *TokenService) AuthenticateScoped(ctx context.Context, rawToken string) (User, pkgauth.ScopedTokenClaims, error) {
	claims, err := pkgauth.VerifyScopedToken(s.scopedSecret, rawToken, s.now())
	if err != nil {
		return User{}, pkgauth.ScopedTokenClaims{}, err
	}
	user, err := s.store.GetUser(ctx, claims.UserID)
	if err != nil {
		return User{}, pkgauth.ScopedTokenClaims{}, fmt.Errorf("token service: get user %s: %w", claims.UserID, err)
	}
	return user, claims, nil
}

func scopedTokenSecret() []byte {
	if raw := strings.TrimSpace(os.Getenv(scopedTokenSecretEnv)); raw != "" {
		return []byte(raw)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("generate scoped token secret: %v", err))
	}
	return secret
}
