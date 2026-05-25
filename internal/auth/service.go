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
	users         UserStore
	logins        LoginIdentityStore
	sessions      SessionStore
	organizations OrganizationStore
	memberships   MembershipStore
	db            *sql.DB
}

// NewAuthService creates an AuthService with all required stores.
func NewAuthService(
	db *sql.DB,
	users UserStore,
	logins LoginIdentityStore,
	sessions SessionStore,
	organizations OrganizationStore,
	memberships MembershipStore,
) *AuthService {
	return &AuthService{
		db:            db,
		users:         users,
		logins:        logins,
		sessions:      sessions,
		organizations: organizations,
		memberships:   memberships,
	}
}

// OIDCLoginResult holds everything the HTTP handler needs after a successful
// OIDC callback.
type OIDCLoginResult struct {
	User         User
	Session      Session
	Membership   Membership
	IsNewUser    bool
	SessionToken string // raw token for the session cookie (not stored in DB)
}

// ProcessOIDCLogin is the single transaction entry point for an OIDC callback.
// It upserts the organization, resolves or creates the user and login identity,
// manages org membership, and creates a new session.
//
// Email auto-linking: if the OIDC identity does not exist yet but the email
// matches an existing User (email_verified must be true from the provider),
// the identity is created for that user instead of creating a new one.
//
// Membership transition: if the user already has a membership in a different
// (backfill-created default) org, that membership is replaced by the OIDC org
// membership. If the old org becomes empty it is deleted.
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
		users:         stores.Users,
		logins:        stores.Logins,
		sessions:      stores.Sessions,
		organizations: stores.Organizations,
		memberships:   stores.Memberships,
		db:            s.db,
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

// processOIDCLoginNoTx is the non-transactional implementation shared by both paths.
func (s *AuthService) processOIDCLoginNoTx(ctx context.Context, ext ExternalIdentity, sessionMgr *SessionManager) (OIDCLoginResult, error) {
	user, isNewUser, err := s.resolveUser(ctx, ext)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: resolve user: %w", err)
	}

	membership, err := s.ensureMembership(ctx, user.ID)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: manage membership: %w", err)
	}

	rawToken, session, err := sessionMgr.Create(ctx, user.ID)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: create session: %w", err)
	}

	return OIDCLoginResult{
		User:         user,
		Session:      session,
		Membership:   membership,
		IsNewUser:    isNewUser,
		SessionToken: rawToken,
	}, nil
}

// PrincipalFromToken resolves a Principal from a raw session token. It hashes
// the token, looks up the session, loads the user and membership, and checks
// is_active. Returns an error when the session is invalid, expired, or the
// membership is inactive.
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

	membership, err := s.memberships.GetUserMembership(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("auth: get membership: %w", err)
	}
	if !membership.IsActive {
		return nil, errors.New("auth: membership is inactive")
	}

	return &Principal{
		UserID:    user.ID,
		OrgID:     membership.OrganizationID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      membership.Role,
	}, nil
}

// GetUserMembership returns the user's current org membership.
func (s *AuthService) GetUserMembership(ctx context.Context, userID string) (Membership, error) {
	return s.memberships.GetUserMembership(ctx, userID)
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

// createOrganization creates a new personal org for the user.
func (s *AuthService) createOrganization(ctx context.Context) (Organization, error) {
	return s.organizations.CreateOrganization(ctx, Organization{
		ID:         uuid.NewString(),
		Name:       "My Organization",
		ExternalID: uuid.NewString(),
		Source:     "stella",
	})
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

	// Email auto-linking: if an existing user has this email, link identity to them.
	if ext.Email != "" {
		if user, err := s.users.GetUserByEmail(ctx, ext.Email); err == nil {
			if _, err := s.logins.CreateLoginIdentity(ctx, LoginIdentity{
				ID:              uuid.NewString(),
				UserID:          user.ID,
				Provider:        ext.Provider,
				ProviderSubject: ext.Subject,
				Email:           ext.Email,
				Name:            ext.Name,
				AvatarURL:       ext.AvatarURL,
				RawClaims:       ext.Claims,
			}); err != nil {
				return User{}, false, fmt.Errorf("auth: create login identity for existing user: %w", err)
			}
			return user, false, nil
		}
	}

	// Create new user + identity.
	newUser, err := s.users.CreateUser(ctx, User{
		ID:        uuid.NewString(),
		Email:     ext.Email,
		Name:      ext.Name,
		AvatarURL: ext.AvatarURL,
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

// ensureMembership returns the user's existing membership or creates a new org
// and membership if they don't have one yet.
func (s *AuthService) ensureMembership(ctx context.Context, userID string) (Membership, error) {
	existing, err := s.memberships.GetUserMembership(ctx, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, err
	}

	org, err := s.createOrganization(ctx)
	if err != nil {
		return Membership{}, fmt.Errorf("auth: create org for new user: %w", err)
	}

	return s.memberships.CreateMembership(ctx, Membership{
		ID:             uuid.NewString(),
		UserID:         userID,
		OrganizationID: org.ID,
		Role:           RoleAdmin,
		IsActive:       true,
	})
}
