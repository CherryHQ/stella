package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/vault"
	pkgauth "github.com/CherryHQ/stella/pkg/auth"
)

const (
	// StellaTokenName is re-exported from the vault package for callers that only
	// import auth.
	StellaTokenName      = vault.StellaTokenName
	autoTokenTTL         = 90 * 24 * time.Hour
	autoTokenRotateAfter = 60 * 24 * time.Hour
	tokenPrefixLength    = 15
	scopedTokenSecretEnv = "STELLA_SCOPED_TOKEN_SECRET"
)

type tokenStore interface {
	CreateUserToken(ctx context.Context, token UserToken) (UserToken, error)
	GetActiveUserTokenByHash(ctx context.Context, tokenHash string) (UserToken, error)
	GetActiveAutoUserToken(ctx context.Context, userID string) (UserToken, error)
	RotateUserToken(ctx context.Context, id string) (int64, error)
	UpdateUserTokenLastUsed(ctx context.Context, id string) (int64, error)
	GetUser(ctx context.Context, id string) (User, error)
}

// VaultWriter writes internal plaintext secrets to the per-user vault.
type VaultWriter interface {
	SetReserved(ctx context.Context, userID string, name string, plaintext string) error
}

type vaultLoader interface {
	LoadEnv(ctx context.Context, userID string) (map[string]string, error)
}

type scopedVaultLoader interface {
	LoadEnvForAgent(ctx context.Context, userID string, agentID string) (map[string]string, error)
}

type envReloader func() (map[string]string, error)

// TokenService owns API token lifecycle and authentication.
type TokenService struct {
	store        tokenStore
	vault        VaultWriter
	now          func() time.Time
	scopedSecret []byte
	mu           sync.Mutex
}

// NewTokenService creates a token service backed by auth persistence and vault writes.
func NewTokenService(store tokenStore, vault VaultWriter) *TokenService {
	return &TokenService{store: store, vault: vault, now: time.Now, scopedSecret: scopedTokenSecret()}
}

// EnsureAutoToken ensures userID has one active auto-generated token whose
// vault plaintext matches the DB token hash. Concurrent callers are serialized
// so the vault never exposes an unrecorded loser token between ensure and env load.
func (s *TokenService) EnsureAutoToken(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.ensureAutoTokenLocked(ctx, userID, nil)
	return err
}

// EnsureAutoTokenEnv ensures the auto token and returns the user's vault env
// under the same lock, so sandbox env injection cannot observe a transient token
// written by a concurrent loser.
//
// The vault is decrypted once up front and reused for both token validation and
// the returned env; it is reloaded only when ensure actually wrote to the vault
// (token created or rotated). This collapses the two full-vault decrypts the
// path used to do per sandbox start into one in the steady state.
func (s *TokenService) EnsureAutoTokenEnv(ctx context.Context, userID string) (map[string]string, error) {
	return s.ensureAutoTokenEnv(ctx, userID, "")
}

// EnsureAutoTokenEnvForAgent ensures the auto token and returns scoped runtime
// env under the same lock, preserving the token/env atomicity guarantee while
// allowing agent-specific vault scopes to participate in sandbox env resolution.
func (s *TokenService) EnsureAutoTokenEnvForAgent(ctx context.Context, userID string, agentID string) (map[string]string, error) {
	return s.ensureAutoTokenEnv(ctx, userID, agentID)
}

func (s *TokenService) ensureAutoTokenEnv(ctx context.Context, userID string, agentID string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	env, reload, err := s.loadEnvForTokenEnsure(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	changed, err := s.ensureAutoTokenLocked(ctx, userID, env)
	if err != nil {
		return nil, err
	}
	if changed {
		env, err = reload()
		if err != nil {
			return nil, err
		}
	}
	return env, nil
}

func (s *TokenService) loadEnvForTokenEnsure(ctx context.Context, userID string, agentID string) (map[string]string, envReloader, error) {
	if loader, ok := s.vault.(scopedVaultLoader); ok {
		reload := func() (map[string]string, error) {
			env, err := loader.LoadEnvForAgent(ctx, userID, agentID)
			if err != nil {
				return nil, fmt.Errorf("token service: load env for user %s agent %s: %w", userID, agentID, err)
			}
			return env, nil
		}
		env, err := reload()
		return env, reload, err
	}
	if loader, ok := s.vault.(vaultLoader); ok {
		reload := func() (map[string]string, error) {
			env, err := loader.LoadEnv(ctx, userID)
			if err != nil {
				return nil, fmt.Errorf("token service: load env for user %s: %w", userID, err)
			}
			return env, nil
		}
		env, err := reload()
		return env, reload, err
	}
	return map[string]string{}, func() (map[string]string, error) { return map[string]string{}, nil }, nil
}

// ensureAutoTokenLocked guarantees an active auto token whose vault plaintext
// matches the DB hash. env, when non-nil, is a pre-decrypted vault snapshot used
// for token validation to avoid a redundant decrypt; pass nil to have validation
// load on demand. It reports whether the vault was written (token created or
// rotated) so callers reusing a snapshot know when to reload.
func (s *TokenService) ensureAutoTokenLocked(ctx context.Context, userID string, env map[string]string) (bool, error) {
	token, err := s.store.GetActiveAutoUserToken(ctx, userID)
	if err == nil {
		if ok, err := s.activeVaultTokenValid(ctx, userID, token, env); err != nil {
			return false, err
		} else if !ok {
			return true, s.rotateAutoToken(ctx, token)
		}
		if s.now().UTC().Before(token.CreatedAt.Add(autoTokenRotateAfter)) {
			return false, nil
		}
		return true, s.rotateAutoToken(ctx, token)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("token service: get active auto token for user %s: %w", userID, err)
	}

	plaintext, err := generateToken()
	if err != nil {
		return false, err
	}
	if err := s.vault.SetReserved(ctx, userID, StellaTokenName, plaintext); err != nil {
		return false, fmt.Errorf("token service: write auto token to vault: %w", err)
	}
	if err := s.createAutoTokenRecord(ctx, userID, plaintext); err != nil {
		return true, s.recoverFromCreateRace(ctx, userID)
	}
	return true, nil
}

// recoverFromCreateRace handles the case where a concurrent EnsureAutoToken
// won the DB insert race. The vault may have been overwritten by the losing
// goroutine, so we rotate the winning token to re-establish consistency.
func (s *TokenService) recoverFromCreateRace(ctx context.Context, userID string) error {
	winner, err := s.store.GetActiveAutoUserToken(ctx, userID)
	if err != nil {
		return fmt.Errorf("token service: recover from race for user %s: %w", userID, err)
	}
	return s.rotateAutoToken(ctx, winner)
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

// Authenticate returns the active user identified by rawToken.
func (s *TokenService) Authenticate(ctx context.Context, rawToken string) (User, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return User{}, pgx.ErrNoRows
	}
	token, err := s.store.GetActiveUserTokenByHash(ctx, hashToken(rawToken))
	if err != nil {
		return User{}, fmt.Errorf("token service: lookup token: %w", err)
	}
	user, err := s.store.GetUser(ctx, token.UserID)
	if err != nil {
		return User{}, fmt.Errorf("token service: get user %s: %w", token.UserID, err)
	}
	if _, err := s.store.UpdateUserTokenLastUsed(ctx, token.ID); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *TokenService) rotateAutoToken(ctx context.Context, token UserToken) error {
	rows, err := s.store.RotateUserToken(ctx, token.ID)
	if err != nil {
		return err
	}
	if rows == 0 {
		_, err := s.store.GetActiveAutoUserToken(ctx, token.UserID)
		return err
	}
	plaintext, err := generateToken()
	if err != nil {
		return err
	}
	if err := s.vault.SetReserved(ctx, token.UserID, StellaTokenName, plaintext); err != nil {
		return fmt.Errorf("token service: write rotated token to vault: %w", err)
	}
	return s.createAutoTokenRecord(ctx, token.UserID, plaintext)
}

func (s *TokenService) createAutoTokenRecord(ctx context.Context, userID string, plaintext string) error {
	expiresAt := s.now().UTC().Add(autoTokenTTL)
	_, err := s.store.CreateUserToken(ctx, UserToken{
		UserID:        userID,
		Name:          StellaTokenName,
		TokenHash:     hashToken(plaintext),
		TokenPrefix:   tokenPrefix(plaintext),
		AutoGenerated: true,
		ExpiresAt:     &expiresAt,
	})
	if err != nil {
		return fmt.Errorf("token service: create auto token record for user %s: %w", userID, err)
	}
	return nil
}

// activeVaultTokenValid checks whether the active vault token is still valid.
// Returns (true, nil) when the vault does not implement vaultLoader
// (e.g. write-only test stubs). env, when non-nil, is a pre-decrypted snapshot
// reused instead of decrypting the vault again.
func (s *TokenService) activeVaultTokenValid(ctx context.Context, userID string, token UserToken, env map[string]string) (bool, error) {
	if _, ok := s.vault.(vaultLoader); !ok {
		return true, nil
	}
	plaintext, ok, err := s.loadVaultToken(ctx, userID, env)
	if err != nil || !ok {
		return ok, err
	}
	return hashToken(plaintext) == token.TokenHash, nil
}

// loadVaultToken returns the auto-token plaintext from env when a snapshot is
// supplied, otherwise it decrypts the vault on demand.
func (s *TokenService) loadVaultToken(ctx context.Context, userID string, env map[string]string) (string, bool, error) {
	if env == nil {
		loader, ok := s.vault.(vaultLoader)
		if !ok {
			return "", false, nil
		}
		var err error
		env, err = loader.LoadEnv(ctx, userID)
		if err != nil {
			return "", false, fmt.Errorf("token service: load vault token: %w", err)
		}
	}
	plaintext, ok := env[StellaTokenName]
	return plaintext, ok && plaintext != "", nil
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

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token service: generate token: %w", err)
	}
	return "stella_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

func tokenPrefix(rawToken string) string {
	if len(rawToken) <= tokenPrefixLength {
		return rawToken
	}
	return rawToken[:tokenPrefixLength]
}
