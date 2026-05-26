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
	invites       InviteStore
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
	invites InviteStore,
) *AuthService {
	return &AuthService{
		db:            db,
		users:         users,
		logins:        logins,
		sessions:      sessions,
		organizations: organizations,
		memberships:   memberships,
		invites:       invites,
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
// (seed-created default) org, that membership is replaced by the OIDC org
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
		invites:       stores.Invites,
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

// CreateInvite generates a new invite link for the given org. Returns the raw
// token (to be shared with the invitee) and the persisted Invite record.
func (s *AuthService) CreateInvite(ctx context.Context, orgID, email, role, invitedBy string, maxUses int, ttl time.Duration) (string, Invite, error) {
	rawToken, err := generateRawToken()
	if err != nil {
		return "", Invite{}, fmt.Errorf("auth: generate invite token: %w", err)
	}

	inv := Invite{
		ID:        uuid.NewString(),
		TokenHash: hashSessionToken(rawToken),
		OrgID:     orgID,
		Email:     email,
		Role:      role,
		Status:    InviteStatusPending,
		MaxUses:   maxUses,
		InvitedBy: invitedBy,
		ExpiresAt: time.Now().Add(ttl),
	}

	created, err := s.invites.CreateInvite(ctx, inv)
	if err != nil {
		return "", Invite{}, fmt.Errorf("auth: create invite: %w", err)
	}
	return rawToken, created, nil
}

// GetInviteInfo looks up an invite by raw token and returns the invite along
// with the target organization. Returns an error if the invite is expired,
// revoked, or fully consumed.
func (s *AuthService) GetInviteInfo(ctx context.Context, rawToken string) (Invite, Organization, error) {
	inv, err := s.invites.GetInviteByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		return Invite{}, Organization{}, fmt.Errorf("auth: invite not found: %w", err)
	}
	if inv.Status != InviteStatusPending {
		return Invite{}, Organization{}, fmt.Errorf("auth: invite is %s", inv.Status)
	}
	if time.Now().After(inv.ExpiresAt) {
		return Invite{}, Organization{}, errors.New("auth: invite expired")
	}
	if inv.UseCount >= inv.MaxUses {
		return Invite{}, Organization{}, errors.New("auth: invite fully consumed")
	}

	org, err := s.organizations.GetOrganization(ctx, inv.OrgID)
	if err != nil {
		return Invite{}, Organization{}, fmt.Errorf("auth: get invite org: %w", err)
	}
	return inv, org, nil
}

// RedeemInvite accepts an invite for the given user, moving them into the
// invite's organization with the invite's role.
func (s *AuthService) RedeemInvite(ctx context.Context, rawToken string, userID string) error {
	inv, _, err := s.GetInviteInfo(ctx, rawToken)
	if err != nil {
		return err
	}

	// Email restriction check.
	if inv.Email != "" {
		user, err := s.users.GetUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("auth: get user for invite: %w", err)
		}
		if user.Email != inv.Email {
			return errors.New("auth: invite restricted to a different email")
		}
	}

	// Check current membership.
	existing, err := s.memberships.GetUserMembership(ctx, userID)
	switch {
	case err == nil:
		if existing.OrganizationID == inv.OrgID {
			if existing.Role != inv.Role {
				if err := s.memberships.UpdateMembershipRole(ctx, existing.ID, inv.Role); err != nil {
					return fmt.Errorf("auth: update membership role: %w", err)
				}
			}
		} else {
			if err := s.memberships.DeleteMembership(ctx, existing.ID); err != nil {
				return fmt.Errorf("auth: delete old membership: %w", err)
			}
			if _, err := s.memberships.CreateMembership(ctx, Membership{
				ID:             uuid.NewString(),
				UserID:         userID,
				OrganizationID: inv.OrgID,
				Role:           inv.Role,
				IsActive:       true,
			}); err != nil {
				return fmt.Errorf("auth: create membership for invite: %w", err)
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.memberships.CreateMembership(ctx, Membership{
			ID:             uuid.NewString(),
			UserID:         userID,
			OrganizationID: inv.OrgID,
			Role:           inv.Role,
			IsActive:       true,
		}); err != nil {
			return fmt.Errorf("auth: create membership for invite: %w", err)
		}
	default:
		return fmt.Errorf("auth: check membership: %w", err)
	}

	return s.invites.ConsumeInvite(ctx, inv.ID, userID)
}

// ListOrgInvites returns all invites for the given organization.
func (s *AuthService) ListOrgInvites(ctx context.Context, orgID string) ([]Invite, error) {
	return s.invites.ListInvitesByOrg(ctx, orgID)
}

// RevokeInvite marks a pending invite as revoked.
func (s *AuthService) RevokeInvite(ctx context.Context, id string) error {
	return s.invites.RevokeInvite(ctx, id)
}

// createOrganization creates a new personal org for the user.
func (s *AuthService) createOrganization(ctx context.Context) (Organization, error) {
	return s.organizations.CreateOrganization(ctx, Organization{
		ID:         uuid.NewString(),
		Name:       DefaultOrgName,
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
// and membership if they don't have one yet. When an unowned org exists (e.g.
// the boot-time seed org), the first user adopts it instead of creating a new
// one so that seeded resources (agents, providers, channels) are visible.
func (s *AuthService) ensureMembership(ctx context.Context, userID string) (Membership, error) {
	existing, err := s.memberships.GetUserMembership(ctx, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, err
	}

	org, err := s.findUnownedOrg(ctx)
	if err != nil {
		org, err = s.createOrganization(ctx)
		if err != nil {
			return Membership{}, fmt.Errorf("auth: create org for new user: %w", err)
		}
	}

	return s.memberships.CreateMembership(ctx, Membership{
		ID:             uuid.NewString(),
		UserID:         userID,
		OrganizationID: org.ID,
		Role:           RoleAdmin,
		IsActive:       true,
	})
}

// findUnownedOrg returns the first organization that has zero members.
func (s *AuthService) findUnownedOrg(ctx context.Context) (Organization, error) {
	orgs, err := s.organizations.ListOrganizations(ctx)
	if err != nil {
		return Organization{}, err
	}
	for _, org := range orgs {
		count, err := s.memberships.CountOrgMembers(ctx, org.ID)
		if err != nil {
			continue
		}
		if count == 0 {
			return org, nil
		}
	}
	return Organization{}, sql.ErrNoRows
}
