package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

const oidcTimeLayout = "2006-01-02 15:04:05"

// OIDCStore implements all five new auth store interfaces using raw SQL against
// the new OIDC tables (auth_user, auth_identity, auth_session, auth_organization,
// auth_membership, channel_identity).
//
// The new tables are not included in the sqlc schema glob to avoid struct name
// conflicts with the legacy auth_users/auth_sessions/auth_identities tables
// during the additive migration period.
type OIDCStore struct {
	db *sql.DB
}

// NewOIDCStore creates an OIDCStore backed by the given database connection.
func NewOIDCStore(db *sql.DB) *OIDCStore {
	return &OIDCStore{db: db}
}

// Ensure OIDCStore satisfies all five new store interfaces at compile time.
var (
	_ auth.UserStore            = (*OIDCStore)(nil)
	_ auth.LoginIdentityStore   = (*OIDCStore)(nil)
	_ auth.ChannelIdentityStore = (*OIDCStore)(nil)
	_ auth.SessionStore         = (*OIDCStore)(nil)
	_ auth.OrganizationStore    = (*OIDCStore)(nil)
	_ auth.MembershipStore      = (*OIDCStore)(nil)
)

func parseOIDCTime(s string) time.Time {
	t, _ := time.ParseInLocation(oidcTimeLayout, s, time.UTC)
	return t
}

// ---- UserStore ----

func (s *OIDCStore) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	const q = `INSERT INTO auth_user (id, email, name, avatar_url, age_public_key, age_private_key)
	           VALUES (?, ?, ?, ?, ?, '') RETURNING id, email, name, avatar_url,
	           default_agent_id, notify_identity_id, age_public_key, age_private_key,
	           created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, u.ID, u.Email, u.Name, u.AvatarURL, u.AgePublicKey)
	return scanUser(row)
}

func (s *OIDCStore) GetUser(ctx context.Context, id string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanUser(row)
}

func (s *OIDCStore) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE email = ?`
	row := s.db.QueryRowContext(ctx, q, email)
	return scanUser(row)
}

func (s *OIDCStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []auth.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *OIDCStore) UpdateUser(ctx context.Context, u auth.User) error {
	const q = `UPDATE auth_user SET name=?, avatar_url=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, u.Name, u.AvatarURL, u.ID)
	return err
}

func (s *OIDCStore) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_user WHERE id=?`, id)
	return err
}

func (s *OIDCStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_user`).Scan(&n)
	return n, err
}

func (s *OIDCStore) UpdateUserAgeKeys(ctx context.Context, userID, publicKey, privateKey string) error {
	const q = `UPDATE auth_user SET age_public_key=?, age_private_key=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, publicKey, privateKey, userID)
	return err
}

func (s *OIDCStore) UpdateUserDefaultAgent(ctx context.Context, userID, agentID string) error {
	const q = `UPDATE auth_user SET default_agent_id=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, agentID, userID)
	return err
}

func (s *OIDCStore) UpdateUserNotifyIdentity(ctx context.Context, userID string, identityID *string) error {
	const q = `UPDATE auth_user SET notify_identity_id=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, identityID, userID)
	return err
}

// scanUser reads a single auth_user row from a *sql.Row or *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(r rowScanner) (auth.User, error) {
	var u auth.User
	var defaultAgentID, notifyIdentityID sql.NullString
	var createdAt, updatedAt string
	err := r.Scan(
		&u.ID, &u.Email, &u.Name, &u.AvatarURL,
		&defaultAgentID, &notifyIdentityID,
		&u.AgePublicKey, &u.AgePrivateKey,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return auth.User{}, fmt.Errorf("auth_user scan: %w", err)
	}
	u.DefaultAgentID = defaultAgentID.String
	if notifyIdentityID.Valid {
		id := notifyIdentityID.String
		u.NotifyIdentityID = &id
	}
	u.CreatedAt = parseOIDCTime(createdAt)
	u.UpdatedAt = parseOIDCTime(updatedAt)
	return u, nil
}

// ---- LoginIdentityStore ----

func (s *OIDCStore) CreateLoginIdentity(ctx context.Context, i auth.LoginIdentity) (auth.LoginIdentity, error) {
	claims, err := json.Marshal(i.RawClaims)
	if err != nil {
		return auth.LoginIdentity{}, err
	}
	const q = `INSERT INTO auth_identity (id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims)
	           VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	           RETURNING id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q,
		i.ID, i.UserID, i.Provider, i.ProviderSubject,
		i.Email, i.Name, i.AvatarURL, string(claims),
	)
	return scanLoginIdentity(row)
}

func (s *OIDCStore) GetLoginIdentityByProvider(ctx context.Context, provider, providerSubject string) (auth.LoginIdentity, error) {
	const q = `SELECT id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at
	           FROM auth_identity WHERE provider=? AND provider_subject=?`
	row := s.db.QueryRowContext(ctx, q, provider, providerSubject)
	return scanLoginIdentity(row)
}

func (s *OIDCStore) ListLoginIdentitiesByUser(ctx context.Context, userID string) ([]auth.LoginIdentity, error) {
	const q = `SELECT id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at
	           FROM auth_identity WHERE user_id=? ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []auth.LoginIdentity
	for rows.Next() {
		id, err := scanLoginIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *OIDCStore) UpdateLoginIdentity(ctx context.Context, i auth.LoginIdentity) error {
	claims, err := json.Marshal(i.RawClaims)
	if err != nil {
		return err
	}
	const q = `UPDATE auth_identity SET email=?, name=?, avatar_url=?, raw_claims=?, updated_at=datetime('now') WHERE id=?`
	_, err = s.db.ExecContext(ctx, q, i.Email, i.Name, i.AvatarURL, string(claims), i.ID)
	return err
}

func scanLoginIdentity(r rowScanner) (auth.LoginIdentity, error) {
	var i auth.LoginIdentity
	var rawClaims, createdAt, updatedAt string
	err := r.Scan(
		&i.ID, &i.UserID, &i.Provider, &i.ProviderSubject,
		&i.Email, &i.Name, &i.AvatarURL, &rawClaims,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return auth.LoginIdentity{}, fmt.Errorf("auth_identity scan: %w", err)
	}
	if rawClaims != "" && rawClaims != "{}" {
		_ = json.Unmarshal([]byte(rawClaims), &i.RawClaims)
	}
	i.CreatedAt = parseOIDCTime(createdAt)
	i.UpdatedAt = parseOIDCTime(updatedAt)
	return i, nil
}

// ---- ChannelIdentityStore ----

func (s *OIDCStore) CreateChannelIdentity(ctx context.Context, i auth.ChannelIdentity) (auth.ChannelIdentity, error) {
	const q = `INSERT INTO channel_identity (id, user_id, platform, external_id, name)
	           VALUES (?, ?, ?, ?, ?)
	           RETURNING id, user_id, platform, external_id, name, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, i.ID, i.UserID, i.Platform, i.ExternalID, i.Name)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentity(ctx context.Context, id string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE id=?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE platform=? AND external_id=?`
	row := s.db.QueryRowContext(ctx, q, platform, externalID)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) ListChannelIdentitiesByUser(ctx context.Context, userID string) ([]auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE user_id=? ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []auth.ChannelIdentity
	for rows.Next() {
		ci, err := scanChannelIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, rows.Err()
}

func (s *OIDCStore) UpdateChannelIdentityExternalID(ctx context.Context, id, externalID string) error {
	const q = `UPDATE channel_identity SET external_id=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, externalID, id)
	return err
}

func (s *OIDCStore) DeleteChannelIdentity(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channel_identity WHERE id=?`, id)
	return err
}

func scanChannelIdentity(r rowScanner) (auth.ChannelIdentity, error) {
	var ci auth.ChannelIdentity
	var createdAt, updatedAt string
	err := r.Scan(&ci.ID, &ci.UserID, &ci.Platform, &ci.ExternalID, &ci.Name, &createdAt, &updatedAt)
	if err != nil {
		return auth.ChannelIdentity{}, fmt.Errorf("channel_identity scan: %w", err)
	}
	ci.CreatedAt = parseOIDCTime(createdAt)
	ci.UpdatedAt = parseOIDCTime(updatedAt)
	return ci, nil
}

// ---- SessionStore ----

func (s *OIDCStore) CreateSession(ctx context.Context, sess auth.Session) (auth.Session, error) {
	expiresAt := sess.ExpiresAt.UTC().Format(oidcTimeLayout)
	const q = `INSERT INTO auth_session (id, user_id, token_hash, expires_at)
	           VALUES (?, ?, ?, ?)
	           RETURNING id, user_id, token_hash, expires_at, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, sess.ID, sess.UserID, sess.TokenHash, expiresAt)
	return scanSession(row)
}

func (s *OIDCStore) GetSessionByTokenHash(ctx context.Context, tokenHash string) (auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at
	           FROM auth_session WHERE token_hash=?`
	row := s.db.QueryRowContext(ctx, q, tokenHash)
	return scanSession(row)
}

func (s *OIDCStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_session WHERE id=?`, id)
	return err
}

func (s *OIDCStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_session WHERE expires_at < datetime('now')`)
	return err
}

func (s *OIDCStore) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_session WHERE user_id=?`, userID)
	return err
}

func (s *OIDCStore) UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error {
	const q = `UPDATE auth_session SET expires_at=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, expiresAt.UTC().Format(oidcTimeLayout), id)
	return err
}

func scanSession(r rowScanner) (auth.Session, error) {
	var sess auth.Session
	var expiresAt, createdAt, updatedAt string
	err := r.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return auth.Session{}, fmt.Errorf("auth_session scan: %w", err)
	}
	sess.ExpiresAt = parseOIDCTime(expiresAt)
	sess.CreatedAt = parseOIDCTime(createdAt)
	sess.UpdatedAt = parseOIDCTime(updatedAt)
	return sess, nil
}

// ---- OrganizationStore ----

func (s *OIDCStore) CreateOrganization(ctx context.Context, o auth.Organization) (auth.Organization, error) {
	const q = `INSERT INTO auth_organization (id, name, external_id, source)
	           VALUES (?, ?, ?, ?)
	           RETURNING id, name, external_id, source, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, o.ID, o.Name, o.ExternalID, o.Source)
	return scanOrganization(row)
}

func (s *OIDCStore) GetOrganization(ctx context.Context, id string) (auth.Organization, error) {
	const q = `SELECT id, name, external_id, source, created_at, updated_at FROM auth_organization WHERE id=?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanOrganization(row)
}

func (s *OIDCStore) GetOrganizationBySource(ctx context.Context, source, externalID string) (auth.Organization, error) {
	const q = `SELECT id, name, external_id, source, created_at, updated_at FROM auth_organization WHERE source=? AND external_id=?`
	row := s.db.QueryRowContext(ctx, q, source, externalID)
	return scanOrganization(row)
}

func (s *OIDCStore) ListOrganizations(ctx context.Context) ([]auth.Organization, error) {
	const q = `SELECT id, name, external_id, source, created_at, updated_at FROM auth_organization ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []auth.Organization
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *OIDCStore) DeleteOrganization(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_organization WHERE id=?`, id)
	return err
}

func scanOrganization(r rowScanner) (auth.Organization, error) {
	var o auth.Organization
	var createdAt, updatedAt string
	err := r.Scan(&o.ID, &o.Name, &o.ExternalID, &o.Source, &createdAt, &updatedAt)
	if err != nil {
		return auth.Organization{}, fmt.Errorf("auth_organization scan: %w", err)
	}
	o.CreatedAt = parseOIDCTime(createdAt)
	o.UpdatedAt = parseOIDCTime(updatedAt)
	return o, nil
}

// ---- MembershipStore ----

func (s *OIDCStore) CreateMembership(ctx context.Context, m auth.Membership) (auth.Membership, error) {
	isActive := 0
	if m.IsActive {
		isActive = 1
	}
	const q = `INSERT INTO auth_membership (id, user_id, organization_id, role, is_active)
	           VALUES (?, ?, ?, ?, ?)
	           RETURNING id, user_id, organization_id, role, is_active, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, m.ID, m.UserID, m.OrganizationID, m.Role, isActive)
	return scanMembership(row)
}

func (s *OIDCStore) GetMembership(ctx context.Context, userID, orgID string) (auth.Membership, error) {
	const q = `SELECT id, user_id, organization_id, role, is_active, created_at, updated_at
	           FROM auth_membership WHERE user_id=? AND organization_id=?`
	row := s.db.QueryRowContext(ctx, q, userID, orgID)
	return scanMembership(row)
}

func (s *OIDCStore) GetUserMembership(ctx context.Context, userID string) (auth.Membership, error) {
	const q = `SELECT id, user_id, organization_id, role, is_active, created_at, updated_at
	           FROM auth_membership WHERE user_id=? LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, userID)
	m, err := scanMembership(row)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Membership{}, sql.ErrNoRows
	}
	return m, err
}

func (s *OIDCStore) UpdateMembershipRole(ctx context.Context, id, role string) error {
	const q = `UPDATE auth_membership SET role=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, role, id)
	return err
}

func (s *OIDCStore) UpdateMembershipActive(ctx context.Context, id string, active bool) error {
	isActive := 0
	if active {
		isActive = 1
	}
	const q = `UPDATE auth_membership SET is_active=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, isActive, id)
	return err
}

func (s *OIDCStore) DeleteMembership(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_membership WHERE id=?`, id)
	return err
}

func (s *OIDCStore) CountOrgMembers(ctx context.Context, orgID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_membership WHERE organization_id=?`, orgID).Scan(&n)
	return n, err
}

func scanMembership(r rowScanner) (auth.Membership, error) {
	var m auth.Membership
	var isActive int
	var createdAt, updatedAt string
	err := r.Scan(&m.ID, &m.UserID, &m.OrganizationID, &m.Role, &isActive, &createdAt, &updatedAt)
	if err != nil {
		return auth.Membership{}, fmt.Errorf("auth_membership scan: %w", err)
	}
	m.IsActive = isActive == 1
	m.CreatedAt = parseOIDCTime(createdAt)
	m.UpdatedAt = parseOIDCTime(updatedAt)
	return m, nil
}
