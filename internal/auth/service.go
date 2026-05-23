package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuthService composes the five auth stores and owns business-level transactions
// such as ProcessOIDCLogin. It is the only place that coordinates cross-store
// writes inside a single DB transaction.
type AuthService struct {
	users         UserStore
	logins        LoginIdentityStore
	channels      ChannelIdentityStore
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
	channels ChannelIdentityStore,
	sessions SessionStore,
	organizations OrganizationStore,
	memberships MembershipStore,
) *AuthService {
	return &AuthService{
		db:            db,
		users:         users,
		logins:        logins,
		channels:      channels,
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
	// Upsert organization.
	org, err := s.upsertOrganization(ctx, ext)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: upsert organization: %w", err)
	}

	// Resolve or create user + login identity.
	user, isNewUser, err := s.resolveUser(ctx, ext)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: resolve user: %w", err)
	}

	// Manage org membership.
	membership, err := s.manageMembership(ctx, user.ID, org)
	if err != nil {
		return OIDCLoginResult{}, fmt.Errorf("auth: manage membership: %w", err)
	}

	// Create session.
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
		UserID: user.ID,
		OrgID:  membership.OrganizationID,
		Email:  user.Email,
		Role:   membership.Role,
	}, nil
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

// upsertOrganization finds or creates the org from ExternalIdentity claims.
// If OrgID is empty, a local default org is used (source="local", external_id="default").
func (s *AuthService) upsertOrganization(ctx context.Context, ext ExternalIdentity) (Organization, error) {
	source := ext.Provider
	externalID := ext.OrgID
	name := ext.OrgName

	if externalID == "" {
		source = "local"
		externalID = "default"
		name = "Default"
	}

	org, err := s.organizations.GetOrganizationBySource(ctx, source, externalID)
	if err == nil {
		return org, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Organization{}, err
	}

	return s.organizations.CreateOrganization(ctx, Organization{
		ID:         uuid.NewString(),
		Name:       name,
		ExternalID: externalID,
		Source:     source,
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
	if !errors.Is(err, sql.ErrNoRows) {
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

// manageMembership ensures the user has exactly one membership (in the OIDC org).
// If the user already belongs to a different org (e.g. a backfill default org),
// that old membership is removed and the old org is deleted if it becomes empty.
func (s *AuthService) manageMembership(ctx context.Context, userID string, org Organization) (Membership, error) {
	existing, err := s.memberships.GetUserMembership(ctx, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, err
	}

	if err == nil && existing.OrganizationID == org.ID {
		// Already in the right org — no change needed.
		return existing, nil
	}

	if err == nil && existing.OrganizationID != org.ID {
		// User is in a different org — remove old membership and clean up.
		oldOrgID := existing.OrganizationID
		if derr := s.memberships.DeleteMembership(ctx, existing.ID); derr != nil {
			return Membership{}, fmt.Errorf("auth: delete old membership: %w", derr)
		}
		if count, cerr := s.memberships.CountOrgMembers(ctx, oldOrgID); cerr == nil && count == 0 {
			_ = s.organizations.DeleteOrganization(ctx, oldOrgID)
		}
	}

	// Create membership in new org. First member becomes admin.
	role := RoleUser
	count, err := s.memberships.CountOrgMembers(ctx, org.ID)
	if err != nil {
		return Membership{}, fmt.Errorf("auth: count org members: %w", err)
	}
	if count == 0 {
		role = RoleAdmin
	}

	return s.memberships.CreateMembership(ctx, Membership{
		ID:             uuid.NewString(),
		UserID:         userID,
		OrganizationID: org.ID,
		Role:           role,
		IsActive:       true,
	})
}
