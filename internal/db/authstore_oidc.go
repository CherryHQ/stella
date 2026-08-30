package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// OIDCStore implements auth store interfaces using raw SQL against the OIDC
// tables (auth_user, auth_identity, auth_session, channel_identity,
// auth_credential).
type OIDCStore struct {
	db    dbQuerier
	rawDB *pgxpool.Pool // non-nil only for the root store; nil for tx-scoped copies
}

// dbQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, allowing OIDCStore to
// operate inside or outside a transaction without code duplication.
type dbQuerier interface {
	Query(ctx context.Context, query string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) pgx.Row
	Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error)
}

// NewOIDCStore creates an OIDCStore backed by the given database connection.
func NewOIDCStore(db *pgxpool.Pool) *OIDCStore {
	return &OIDCStore{db: db, rawDB: db}
}

// BeginAuthTx starts a database transaction and returns tx-scoped copies of
// all auth stores. Implements auth.Transactioner so AuthService.ProcessOIDCLogin
// can run the entire login flow atomically.
func (s *OIDCStore) BeginAuthTx(ctx context.Context) (auth.AuthStores, func() error, func(), error) {
	if s.rawDB == nil {
		return auth.AuthStores{}, nil, nil, fmt.Errorf("oidcstore: BeginAuthTx called on a tx-scoped store")
	}
	tx, err := s.rawDB.Begin(ctx)
	if err != nil {
		return auth.AuthStores{}, nil, nil, err
	}
	txStore := &OIDCStore{db: tx}
	stores := auth.AuthStores{
		Users:       txStore,
		Logins:      txStore,
		Channels:    txStore,
		Admins:      txStore,
		ActiveUsers: txStore,
		Sessions:    txStore,
		Credentials: txStore,
	}
	// pgx.Tx owns its pooled connection: Commit/Rollback release it. Rollback
	// after a successful Commit is a no-op that returns ErrTxClosed, so the
	// caller can always defer rollback without tracking commit state.
	commit := func() error { return tx.Commit(ctx) }
	rollback := func() { _ = tx.Rollback(ctx) }
	return stores, commit, rollback, nil
}

// Ensure OIDCStore satisfies auth.Transactioner at compile time.
var _ auth.Transactioner = (*OIDCStore)(nil)

// Ensure OIDCStore satisfies all store interfaces at compile time.
var (
	_ auth.UserStore                      = (*OIDCStore)(nil)
	_ auth.UserRoleConditionalDeactivator = (*OIDCStore)(nil)
	_ auth.LoginIdentityStore             = (*OIDCStore)(nil)
	_ auth.ChannelIdentityStore           = (*OIDCStore)(nil)
	_ auth.ActiveAdminStore               = (*OIDCStore)(nil)
	_ auth.ActiveUserStore                = (*OIDCStore)(nil)
	_ auth.SessionStore                   = (*OIDCStore)(nil)
	_ auth.CredentialStore                = (*OIDCStore)(nil)
)

// ---- Agent assignments ----

func (s *OIDCStore) AssignAgent(ctx context.Context, userID, agentID string) error {
	return s.mutateAgentAssignment(ctx, userID, agentID, `
		WITH assigned AS (
			INSERT INTO auth_user_agent (user_id, agent_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
			RETURNING agent_id
		)
		UPDATE agent SET updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		WHERE id IN (SELECT agent_id FROM assigned)`, "assign")
}

func (s *OIDCStore) RemoveAgent(ctx context.Context, userID, agentID string) error {
	return s.mutateAgentAssignment(ctx, userID, agentID, `
		WITH removed AS (
			DELETE FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2
			RETURNING agent_id
		)
		UPDATE agent SET updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		WHERE id IN (SELECT agent_id FROM removed)`, "remove")
}

// mutateAgentAssignment advances the Agent version when an assignment relation
// changes. Root and transaction-scoped stores take the relation lock shared
// with the system→restricted transition; the latter then retain their caller's
// transaction boundary and the same version invalidation.
func (s *OIDCStore) mutateAgentAssignment(ctx context.Context, userID, agentID, query, action string) error {
	if s.rawDB == nil {
		if tx, ok := s.db.(pgx.Tx); ok {
			if err := AdvisoryXactLock(ctx, tx, AgentAssignmentLockKey(userID, agentID)); err != nil {
				return fmt.Errorf("lock agent assignment %q for user %s: %w", agentID, userID, err)
			}
		}
		if _, err := s.db.Exec(ctx, query, userID, agentID); err != nil {
			return fmt.Errorf("%s agent %q for user %s: %w", action, agentID, userID, err)
		}
		return nil
	}
	tx, err := s.rawDB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s agent %q for user %s: %w", action, agentID, userID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // successful commit makes rollback inert
	if err := AdvisoryXactLock(ctx, tx, AgentAssignmentLockKey(userID, agentID)); err != nil {
		return fmt.Errorf("lock agent assignment %q for user %s: %w", agentID, userID, err)
	}
	if _, err := tx.Exec(ctx, query, userID, agentID); err != nil {
		return fmt.Errorf("%s agent %q for user %s: %w", action, agentID, userID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s agent %q for user %s: %w", action, agentID, userID, err)
	}
	return nil
}

func (s *OIDCStore) ListUserAgentIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT agent_id FROM auth_user_agent WHERE user_id=$1 ORDER BY agent_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user agents for user %s: %w", userID, err)
	}
	defer func() { rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *OIDCStore) ListAgentUserIDs(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT user_id FROM auth_user_agent WHERE agent_id=$1 ORDER BY user_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent users for agent %q: %w", agentID, err)
	}
	defer func() { rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- UserStore ----

func (s *OIDCStore) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	if u.Role == "" {
		u.Role = auth.RoleUser
	}
	const q = `INSERT INTO auth_user (id, email, name, avatar_url, role, age_public_key, age_private_key)
	           VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, email, name, avatar_url, role, is_active,
	           default_agent_id, notify_identity_id, age_public_key, age_private_key,
	           created_at, updated_at`
	row := s.db.QueryRow(ctx, q, u.ID, u.Email, u.Name, u.AvatarURL, u.Role, u.AgePublicKey, u.AgePrivateKey)
	return scanUser(row)
}

func (s *OIDCStore) GetUser(ctx context.Context, id string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE id = $1`
	row := s.db.QueryRow(ctx, q, id)
	return scanUserResult(row)
}

func (s *OIDCStore) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE email = $1`
	row := s.db.QueryRow(ctx, q, email)
	return scanUserResult(row)
}

// scanUser maps a missing row to auth.ErrNotFound so callers can distinguish
// "no such user" from a real scan failure.
func scanUserResult(r rowScanner) (auth.User, error) {
	u, err := scanUser(r)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	return u, err
}

func (s *OIDCStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user ORDER BY created_at ASC`
	return s.queryUsers(ctx, q)
}

func (s *OIDCStore) ListUsersPaged(ctx context.Context, limit, offset int64) ([]auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user ORDER BY created_at ASC LIMIT $1 OFFSET $2`
	return s.queryUsers(ctx, q, limit, offset)
}

func (s *OIDCStore) queryUsers(ctx context.Context, q string, args ...any) ([]auth.User, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { rows.Close() }()
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

func (s *OIDCStore) ListActiveUserIDs(ctx context.Context) ([]string, error) {
	const q = `SELECT id FROM auth_user WHERE is_active = true ORDER BY id`
	rows, err := s.db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *OIDCStore) UpdateUser(ctx context.Context, u auth.User) error {
	const q = `UPDATE auth_user SET name=$1, avatar_url=$2, updated_at=now() WHERE id=$3`
	_, err := s.db.Exec(ctx, q, u.Name, u.AvatarURL, u.ID)
	return err
}

func (s *OIDCStore) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM auth_user WHERE id=$1`, id)
	return err
}

func (s *OIDCStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM auth_user`).Scan(&n)
	return n, err
}

// LockActiveAdmin holds a row lock on an active administrator until the current
// transaction commits. The lock prevents enrollment from observing an admin
// that is concurrently deactivated or demoted.
func (s *OIDCStore) LockActiveAdmin(ctx context.Context) error {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM auth_user WHERE role=$1 AND is_active=true LIMIT 1 FOR SHARE`, auth.RoleAdmin).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	return err
}

// GetActiveUserForShare locks an active user until the current transaction
// completes so identity enrollment cannot race account deactivation.
func (s *OIDCStore) GetActiveUserForShare(ctx context.Context, id string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE id = $1 AND is_active = true FOR SHARE`
	return scanUserResult(s.db.QueryRow(ctx, q, id))
}

func (s *OIDCStore) UpdateUserAgeKeys(ctx context.Context, userID, publicKey, privateKey string) error {
	const q = `UPDATE auth_user SET age_public_key=$1, age_private_key=$2, updated_at=now() WHERE id=$3`
	_, err := s.db.Exec(ctx, q, publicKey, privateKey, userID)
	return err
}

// GetVaultUser returns the age key fields for a user. Satisfies vault.DB.
func (s *OIDCStore) GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return sqlc.VaultUser{}, err
	}
	return sqlc.VaultUser{AgePublicKey: u.AgePublicKey, AgePrivateKey: u.AgePrivateKey}, nil
}

func (s *OIDCStore) UpdateUserDefaultAgent(ctx context.Context, userID, agentID string) error {
	const q = `UPDATE auth_user SET default_agent_id=$1, updated_at=now() WHERE id=$2`
	_, err := s.db.Exec(ctx, q, agentID, userID)
	return err
}

func (s *OIDCStore) UpdateUserNotifyIdentity(ctx context.Context, userID string, identityID *string) error {
	const q = `UPDATE auth_user SET notify_identity_id=$1, updated_at=now() WHERE id=$2`
	_, err := s.db.Exec(ctx, q, identityID, userID)
	return err
}

func (s *OIDCStore) UpdateUserRole(ctx context.Context, userID string, role string) error {
	const q = `UPDATE auth_user SET role = $1, updated_at = now() WHERE id = $2`
	_, err := s.db.Exec(ctx, q, role, userID)
	return err
}

func (s *OIDCStore) UpdateUserActive(ctx context.Context, userID string, isActive bool) error {
	const q = `UPDATE auth_user SET is_active = $1, updated_at = now() WHERE id = $2`
	_, err := s.db.Exec(ctx, q, isActive, userID)
	return err
}

// DeactivateUserIfUserRole is the conditional write used when a caller must
// never deactivate an administrator. PostgreSQL serializes this UPDATE with a
// concurrent role promotion on the same row.
func (s *OIDCStore) DeactivateUserIfUserRole(ctx context.Context, userID string) (bool, error) {
	const q = `UPDATE auth_user SET is_active = false, updated_at = now() WHERE id = $1 AND role = 'user'`
	tag, err := s.db.Exec(ctx, q, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// scanUser reads a single auth_user row from a *sql.Row or *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(r rowScanner) (auth.User, error) {
	var u auth.User
	var defaultAgentID, notifyIdentityID pgtype.Text
	var createdAt, updatedAt time.Time
	err := r.Scan(
		&u.ID, &u.Email, &u.Name, &u.AvatarURL,
		&u.Role, &u.IsActive,
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
	u.CreatedAt = createdAt.UTC()
	u.UpdatedAt = updatedAt.UTC()
	return u, nil
}

// ---- LoginIdentityStore ----

func (s *OIDCStore) CreateLoginIdentity(ctx context.Context, i auth.LoginIdentity) (auth.LoginIdentity, error) {
	claims, err := json.Marshal(i.RawClaims)
	if err != nil {
		return auth.LoginIdentity{}, err
	}
	const q = `INSERT INTO auth_identity (id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	           RETURNING id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at`
	row := s.db.QueryRow(ctx, q,
		i.ID, i.UserID, i.Provider, i.ProviderSubject,
		i.Email, i.Name, i.AvatarURL, string(claims),
	)
	return scanLoginIdentity(row)
}

func (s *OIDCStore) GetLoginIdentityByProvider(ctx context.Context, provider, providerSubject string) (auth.LoginIdentity, error) {
	const q = `SELECT id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at
	           FROM auth_identity WHERE provider=$1 AND provider_subject=$2`
	row := s.db.QueryRow(ctx, q, provider, providerSubject)
	id, err := scanLoginIdentity(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.LoginIdentity{}, auth.ErrNotFound
	}
	return id, err
}

func (s *OIDCStore) ListLoginIdentitiesByUser(ctx context.Context, userID string) ([]auth.LoginIdentity, error) {
	const q = `SELECT id, user_id, provider, provider_subject, email, name, avatar_url, raw_claims, created_at, updated_at
	           FROM auth_identity WHERE user_id=$1 ORDER BY created_at ASC`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { rows.Close() }()
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
	const q = `UPDATE auth_identity SET email=$1, name=$2, avatar_url=$3, raw_claims=$4, updated_at=now() WHERE id=$5`
	_, err = s.db.Exec(ctx, q, i.Email, i.Name, i.AvatarURL, string(claims), i.ID)
	return err
}

func scanLoginIdentity(r rowScanner) (auth.LoginIdentity, error) {
	var i auth.LoginIdentity
	var rawClaims string
	var createdAt, updatedAt time.Time
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
	i.CreatedAt = createdAt.UTC()
	i.UpdatedAt = updatedAt.UTC()
	return i, nil
}

// ---- ChannelIdentityStore ----

func (s *OIDCStore) CreateChannelIdentity(ctx context.Context, i auth.ChannelIdentity) (auth.ChannelIdentity, error) {
	const q = `INSERT INTO channel_identity (id, user_id, platform, external_id, name)
	           VALUES ($1, $2, $3, $4, $5)
	           RETURNING id, user_id, platform, external_id, name, created_at, updated_at`
	row := s.db.QueryRow(ctx, q, i.ID, i.UserID, i.Platform, i.ExternalID, i.Name)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentity(ctx context.Context, id string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE id=$1`
	row := s.db.QueryRow(ctx, q, id)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE platform=$1 AND external_id=$2`
	row := s.db.QueryRow(ctx, q, platform, externalID)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) ListChannelIdentitiesByUser(ctx context.Context, userID string) ([]auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM channel_identity WHERE user_id=$1 ORDER BY created_at ASC`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { rows.Close() }()
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
	const q = `UPDATE channel_identity SET external_id=$1, updated_at=now() WHERE id=$2`
	_, err := s.db.Exec(ctx, q, externalID, id)
	return err
}

func (s *OIDCStore) DeleteChannelIdentity(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM channel_identity WHERE id=$1`, id)
	return err
}

func scanChannelIdentity(r rowScanner) (auth.ChannelIdentity, error) {
	var ci auth.ChannelIdentity
	var createdAt, updatedAt time.Time
	err := r.Scan(&ci.ID, &ci.UserID, &ci.Platform, &ci.ExternalID, &ci.Name, &createdAt, &updatedAt)
	if err != nil {
		return auth.ChannelIdentity{}, fmt.Errorf("channel_identity scan: %w", err)
	}
	ci.CreatedAt = createdAt.UTC()
	ci.UpdatedAt = updatedAt.UTC()
	return ci, nil
}

// ---- SessionStore ----

func (s *OIDCStore) CreateSession(ctx context.Context, sess auth.Session) (auth.Session, error) {
	const q = `INSERT INTO auth_session (id, user_id, token_hash, expires_at)
	           VALUES ($1, $2, $3, $4)
	           RETURNING id, user_id, token_hash, expires_at, created_at, updated_at`
	row := s.db.QueryRow(ctx, q, sess.ID, sess.UserID, sess.TokenHash, sess.ExpiresAt.UTC())
	return scanSession(row)
}

func (s *OIDCStore) GetSessionByTokenHash(ctx context.Context, tokenHash string) (auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at
	           FROM auth_session WHERE token_hash=$1`
	row := s.db.QueryRow(ctx, q, tokenHash)
	return scanSession(row)
}

func (s *OIDCStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM auth_session WHERE id=$1`, id)
	return err
}

func (s *OIDCStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM auth_session WHERE expires_at < now()`)
	return err
}

func (s *OIDCStore) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM auth_session WHERE user_id=$1`, userID)
	return err
}

func (s *OIDCStore) UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error {
	const q = `UPDATE auth_session SET expires_at=$1, updated_at=now() WHERE id=$2`
	_, err := s.db.Exec(ctx, q, expiresAt.UTC(), id)
	return err
}

func (s *OIDCStore) GetSession(ctx context.Context, id string) (auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at FROM auth_session WHERE id=$1`
	return scanSession(s.db.QueryRow(ctx, q, id))
}

func (s *OIDCStore) ListSessionsByUser(ctx context.Context, userID string) ([]auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at
	           FROM auth_session WHERE user_id=$1 AND expires_at > now()
	           ORDER BY created_at DESC`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { rows.Close() }()
	var out []auth.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func scanSession(r rowScanner) (auth.Session, error) {
	var sess auth.Session
	var expiresAt, createdAt, updatedAt time.Time
	err := r.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return auth.Session{}, fmt.Errorf("auth_session scan: %w", err)
	}
	sess.ExpiresAt = expiresAt.UTC()
	sess.CreatedAt = createdAt.UTC()
	sess.UpdatedAt = updatedAt.UTC()
	return sess, nil
}

// ---- CredentialStore ----

func (s *OIDCStore) CreateCredential(ctx context.Context, c auth.Credential) (auth.Credential, error) {
	const q = `INSERT INTO auth_credential (id, user_id, password_hash)
	           VALUES ($1, $2, $3) RETURNING id, user_id, password_hash, created_at, updated_at`
	row := s.db.QueryRow(ctx, q, c.ID, c.UserID, c.PasswordHash)
	return scanCredential(row)
}

func (s *OIDCStore) GetCredentialByUserID(ctx context.Context, userID string) (auth.Credential, error) {
	const q = `SELECT id, user_id, password_hash, created_at, updated_at
	           FROM auth_credential WHERE user_id = $1`
	row := s.db.QueryRow(ctx, q, userID)
	c, err := scanCredential(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, err
}

func (s *OIDCStore) UpdateCredentialHash(ctx context.Context, userID, passwordHash string) error {
	const q = `UPDATE auth_credential SET password_hash = $1, updated_at = now() WHERE user_id = $2`
	res, err := s.db.Exec(ctx, q, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("auth_credential update: %w", err)
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("auth_credential update: user %s not found", userID)
	}
	return nil
}

func (s *OIDCStore) DeleteCredential(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM auth_credential WHERE user_id = $1`, userID)
	return err
}

func scanCredential(r rowScanner) (auth.Credential, error) {
	var c auth.Credential
	var createdAt, updatedAt time.Time
	if err := r.Scan(&c.ID, &c.UserID, &c.PasswordHash, &createdAt, &updatedAt); err != nil {
		return auth.Credential{}, fmt.Errorf("auth_credential scan: %w", err)
	}
	c.CreatedAt = createdAt.UTC()
	c.UpdatedAt = updatedAt.UTC()
	return c, nil
}
