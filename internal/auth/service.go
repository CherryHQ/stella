package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuthService composes the auth stores and owns business-level transactions
// such as ProcessOIDCLogin. It is the only place that coordinates cross-store
// writes inside a single DB transaction.
type AuthService struct {
	users    UserStore
	logins   LoginIdentityStore
	sessions SessionStore
	db       *sql.DB
}

// NewAuthService creates an AuthService with all required stores.
func NewAuthService(
	db *sql.DB,
	users UserStore,
	logins LoginIdentityStore,
	sessions SessionStore,
) *AuthService {
	return &AuthService{
		db:       db,
		users:    users,
		logins:   logins,
		sessions: sessions,
	}
}

// OIDCLoginResult holds everything the HTTP handler needs after a successful
// login callback.
type OIDCLoginResult struct {
	User         User
	Session      Session
	IsNewUser    bool
	SessionToken string // raw token for the session cookie (not stored in DB)
}

// ProcessOIDCLogin is the single transaction entry point for external identity callbacks.
// It resolves or creates the user and login identity, then creates a new session.
func (s *AuthService) ProcessOIDCLogin(ctx context.Context, ext ExternalIdentity, sessionMgr *SessionManager) (OIDCLoginResult, error) {
	// Use a real DB transaction when the store supports it (OIDCStore does).
	if txner, ok := s.users.(Transactioner); ok {
		return s.processOIDCLoginTx(ctx, txner, ext, sessionMgr)
	}
	return s.processOIDCLoginNoTx(ctx, ext, sessionMgr)
}

// processOIDCLoginTx runs the full login flow in a single DB transaction.
func (s *AuthService) processOIDCLoginTx(ctx context.Context, txner Transactioner, ext ExternalIdentity, sessionMgr *SessionManager) (OIDCLoginResult, error) {
	stores, commit, rollback, err := txner.BeginAuthTx(ctx)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: begin login tx: %w", err)
	}
	defer rollback()

	txSvc := &AuthService{
		users:    stores.Users,
		logins:   stores.Logins,
		sessions: stores.Sessions,
		db:       s.db,
	}
	// Session insert must also run inside the transaction.
	txSessionMgr := sessionMgr.WithStore(stores.Sessions)

	result, err := txSvc.processOIDCLoginNoTx(ctx, ext, txSessionMgr)
	if err != nil {
		return OIDCLoginResult{}, err
	}
	if err := commit(); err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: commit login tx: %w", err)
	}

	return result, nil
}

// CreateSessionForUser creates a login session for a user that has already been
// authenticated by Stella itself, such as local password login.
func (s *AuthService) CreateSessionForUser(ctx context.Context, userID string, sessionMgr *SessionManager) (OIDCLoginResult, error) {
	user, err := s.users.GetUser(ctx, userID)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: get user: %w", err)
	}
	if !user.IsActive {
		return OIDCLoginResult{}, errors.New("auth: user is deactivated")
	}
	rawToken, session, err := sessionMgr.Create(ctx, user.ID)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: create session: %w", err)
	}
	return OIDCLoginResult{User: user, Session: session, SessionToken: rawToken}, nil
}

// processOIDCLoginNoTx is the non-transactional implementation shared by both paths.
func (s *AuthService) processOIDCLoginNoTx(ctx context.Context, ext ExternalIdentity, sessionMgr *SessionManager) (OIDCLoginResult, error) {
	user, isNewUser, err := s.resolveUser(ctx, ext)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: resolve user: %w", err)
	}

	rawToken, session, err := sessionMgr.Create(ctx, user.ID)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: create session: %w", err)
	}

	return OIDCLoginResult{
		User:         user,
		Session:      session,
		IsNewUser:    isNewUser,
		SessionToken: rawToken,
	}, nil
}

// PrincipalFromToken resolves a Principal from a raw session token.
func (s *AuthService) PrincipalFromToken(ctx context.Context, rawToken string) (*Principal, error) {
	session, err := s.sessions.GetSessionByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		return nil, fmt.Errorf("auth: session not found: %w", err)
	}
	if time.Now().After(session.ExpiresAt) {
		_ = s.sessions.DeleteSession(ctx, session.ID)
		return nil, errors.New("auth: session expired")
	}

	user, err := s.users.GetUser(ctx, session.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: get user: %w", err)
	}

	if !user.IsActive {
		return nil, errors.New("auth: user is deactivated")
	}

	return &Principal{
		UserID:    user.ID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      user.Role,
	}, nil
}

// UpdateUserAgeKeys stores vault age keys for userID in the OIDC user table.
func (s *AuthService) UpdateUserAgeKeys(ctx context.Context, userID, publicKey, privateKey string) error {
	return s.users.UpdateUserAgeKeys(ctx, userID, publicKey, privateKey)
}

// Logout deletes the session identified by rawToken.
func (s *AuthService) Logout(ctx context.Context, rawToken string) error {
	session, err := s.sessions.GetSessionByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // already gone
		}
		return fmt.Errorf("auth: logout lookup: %w", err)
	}
	return s.sessions.DeleteSession(ctx, session.ID)
}

// resolveUser finds the existing user by login identity or email, or creates a new one.
// Returns (user, isNewUser, error).
func (s *AuthService) resolveUser(ctx context.Context, ext ExternalIdentity) (User, bool, error) {
	// Fast path: identity already exists.
	identity, err := s.logins.GetLoginIdentityByProvider(ctx, ext.Provider, ext.Subject)
	if err == nil {
		user, err := s.users.GetUser(ctx, identity.UserID)
		if err != nil {
			return User{}, false, err
		}
		// Update identity metadata.
		identity.Email = ext.Email
		identity.Name = ext.Name
		identity.AvatarURL = ext.AvatarURL
		identity.RawClaims = ext.Claims
		_ = s.logins.UpdateLoginIdentity(ctx, identity)
		return user, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrNotFound) {
		return User{}, false, err
	}

	// Create new user + identity. First user becomes admin. Stella deliberately
	// does not silently link external identities by matching email: provider-specific
	// email verification strength varies, and linking needs explicit user intent.
	count, err := s.users.CountUsers(ctx)
	if err != nil {
		return User{}, false, fmt.Errorf("auth: count users: %w", err)
	}
	role := RoleUser
	if count == 0 {
		role = RoleAdmin
	}
	newUser, err := s.users.CreateUser(ctx, User{
		ID:        uuid.NewString(),
		Email:     ext.Email,
		Name:      ext.Name,
		AvatarURL: ext.AvatarURL,
		Role:      role,
	})
	if err != nil {
		return User{}, false, fmt.Errorf("auth: create user: %w", err)
	}

	if _, err := s.logins.CreateLoginIdentity(ctx, LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          newUser.ID,
		Provider:        ext.Provider,
		ProviderSubject: ext.Subject,
		Email:           ext.Email,
		Name:            ext.Name,
		AvatarURL:       ext.AvatarURL,
		RawClaims:       ext.Claims,
	}); err != nil {
		_ = s.users.DeleteUser(ctx, newUser.ID)
		return User{}, false, fmt.Errorf("auth: create login identity: %w", err)
	}

	return newUser, true, nil
}
