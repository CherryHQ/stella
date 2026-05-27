package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OrgSeeder seeds all default resources (settings, policies) for a newly created org.
type OrgSeeder interface {
	SeedOrg(ctx context.Context, orgID string) error
}

// UserSeeder initializes per-user resources after org seed is complete.
type UserSeeder interface {
	SeedUser(ctx context.Context, userID, orgID string) error
}

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
	seeder        OrgSeeder
	userSeeder    UserSeeder
	orgInit       OrgInitializer
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

// SetOrgSeeder registers a seeder that populates default resources when a new org is created.
func (s *AuthService) SetOrgSeeder(seeder OrgSeeder) { s.seeder = seeder }

// SetUserSeeder registers a seeder for per-user resources (auto-token, default agent).
func (s *AuthService) SetUserSeeder(seeder UserSeeder) { s.userSeeder = seeder }

// OrgInitializer is called after successful login to ensure per-org runtime is started.
type OrgInitializer interface {
	EnsureStarted(ctx context.Context, orgID string) error
}

// SetOrgInitializer registers a callback to initialize per-org runtime after login.
func (s *AuthService) SetOrgInitializer(init OrgInitializer) { s.orgInit = init }

// InitOrg initializes per-org runtime for the given org, if an initializer is set.
func (s *AuthService) InitOrg(ctx context.Context, orgID string) error {
	if s.orgInit != nil {
		return s.orgInit.EnsureStarted(ctx, orgID)
	}
	return nil
}

// OIDCLoginResult holds everything the HTTP handler needs after a successful
// OIDC callback.
type OIDCLoginResult struct {
	User         User
	Session      Session
	Membership   *Membership // nil when the user has no org yet (needs onboarding)
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

	// Look up existing membership but don't auto-create one.
	var membership *Membership
	if m, err := s.memberships.GetUserMembership(ctx, user.ID); err == nil {
		membership = &m
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OIDCLoginResult{}, fmt.Errorf("auth: get membership: %w", err)
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

// PrincipalFromToken resolves a Principal from a raw session token. Returns an
// error when the session is invalid, expired, the membership is missing, or
// the membership is inactive.
func (s *AuthService) PrincipalFromToken(ctx context.Context, rawToken string) (*Principal, error) {
	p, hasMembership, err := s.PrincipalFromTokenPartial(ctx, rawToken)
	if err != nil {
		return nil, err
	}
	if !hasMembership {
		return nil, errors.New("auth: no membership")
	}
	return p, nil
}

// PrincipalFromTokenPartial resolves a Principal from a raw session token,
// tolerating a missing membership. Returns (principal, hasMembership, error).
// When hasMembership is false the principal has OrgID="" and Role="".
func (s *AuthService) PrincipalFromTokenPartial(ctx context.Context, rawToken string) (*Principal, bool, error) {
	session, err := s.sessions.GetSessionByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		return nil, false, fmt.Errorf("auth: session not found: %w", err)
	}
	if time.Now().After(session.ExpiresAt) {
		_ = s.sessions.DeleteSession(ctx, session.ID)
		return nil, false, errors.New("auth: session expired")
	}

	user, err := s.users.GetUser(ctx, session.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("auth: get user: %w", err)
	}

	membership, err := s.memberships.GetUserMembership(ctx, user.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &Principal{
				UserID:    user.ID,
				Email:     user.Email,
				Name:      user.Name,
				AvatarURL: user.AvatarURL,
			}, false, nil
		}
		return nil, false, fmt.Errorf("auth: get membership: %w", err)
	}
	if !membership.IsActive {
		return nil, false, errors.New("auth: membership is inactive")
	}

	return &Principal{
		UserID:    user.ID,
		OrgID:     membership.OrganizationID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      membership.Role,
	}, true, nil
}

// EnsureMembership returns the user's existing membership or creates a new
// workspace if they don't have one yet.
func (s *AuthService) EnsureMembership(ctx context.Context, userID string) (Membership, error) {
	m, err := s.memberships.GetUserMembership(ctx, userID)
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, fmt.Errorf("auth: get membership: %w", err)
	}
	return s.CreateWorkspace(ctx, userID, "")
}

// CreateWorkspace creates a new org and membership for a user who has none.
func (s *AuthService) CreateWorkspace(ctx context.Context, userID, orgName string) (Membership, error) {
	if _, err := s.memberships.GetUserMembership(ctx, userID); err == nil {
		return Membership{}, errors.New("auth: user already has a membership")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, fmt.Errorf("auth: check membership: %w", err)
	}

	if orgName == "" {
		orgName = DefaultOrgName
	}
	org, err := s.organizations.CreateOrganization(ctx, Organization{
		ID:         uuid.NewString(),
		Name:       orgName,
		ExternalID: uuid.NewString(),
		Source:     "stella",
	})
	if err != nil {
		return Membership{}, fmt.Errorf("auth: create org: %w", err)
	}

	m, err := s.memberships.CreateMembership(ctx, Membership{
		ID:             uuid.NewString(),
		UserID:         userID,
		OrganizationID: org.ID,
		Role:           RoleAdmin,
		IsActive:       true,
	})
	if err != nil {
		return Membership{}, fmt.Errorf("auth: create membership: %w", err)
	}

	if s.seeder != nil {
		if err := s.seeder.SeedOrg(ctx, org.ID); err != nil {
			return Membership{}, fmt.Errorf("auth: seed new org: %w", err)
		}
	}
	if s.userSeeder != nil {
		if err := s.userSeeder.SeedUser(ctx, userID, org.ID); err != nil {
			return Membership{}, fmt.Errorf("auth: seed user: %w", err)
		}
	}

	return m, nil
}

// ListPendingInvitesForEmail returns pending invites matching the given email,
// each paired with the organization name.
func (s *AuthService) ListPendingInvitesForEmail(ctx context.Context, email string) ([]InviteWithOrg, error) {
	if email == "" {
		return nil, nil
	}
	invites, err := s.invites.ListPendingInvitesByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("auth: list pending invites: %w", err)
	}
	out := make([]InviteWithOrg, 0, len(invites))
	for _, inv := range invites {
		orgName := ""
		if org, err := s.organizations.GetOrganization(ctx, inv.OrgID); err == nil {
			orgName = org.Name
		}
		out = append(out, InviteWithOrg{Invite: inv, OrgName: orgName})
	}
	return out, nil
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

	// One-user-one-org: reject invite if user already belongs to a different org.
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
			return errors.New("auth: user already belongs to a different organization")
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
		if s.userSeeder != nil {
			if err := s.userSeeder.SeedUser(ctx, userID, inv.OrgID); err != nil {
				return fmt.Errorf("auth: seed user after invite: %w", err)
			}
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
