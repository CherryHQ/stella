package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const oidcTimeLayout = "2006-01-02 15:04:05"

// OIDCStore implements auth store interfaces using raw SQL against the OIDC
// tables (auth_user, auth_identity, auth_session, plugin_channel_identity,
// auth_credential, oidc_code, oidc_access_token).
type OIDCStore struct {
	db    dbQuerier
	rawDB *sql.DB // non-nil only for the root store; nil for tx-scoped copies
}

// dbQuerier is satisfied by both *sql.DB and *sql.Tx, allowing OIDCStore to
// operate inside or outside a transaction without code duplication.
type dbQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// NewOIDCStore creates an OIDCStore backed by the given database connection.
func NewOIDCStore(db *sql.DB) *OIDCStore {
	return &OIDCStore{db: db, rawDB: db}
}

// withTx returns a copy of OIDCStore that runs all queries through tx.
func (s *OIDCStore) withTx(tx *sql.Tx) *OIDCStore {
	return &OIDCStore{db: tx}
}

// BeginAuthTx starts a database transaction and returns tx-scoped copies of
// all auth stores. Implements auth.Transactioner so AuthService.ProcessOIDCLogin
// can run the entire login flow atomically.
func (s *OIDCStore) BeginAuthTx(ctx context.Context) (auth.AuthStores, func() error, func(), error) {
	if s.rawDB == nil {
		return auth.AuthStores{}, nil, nil, fmt.Errorf("oidcstore: BeginAuthTx called on a tx-scoped store")
	}
	tx, err := s.rawDB.BeginTx(ctx, nil)
	if err != nil {
		return auth.AuthStores{}, nil, nil, err
	}
	txStore := s.withTx(tx)
	stores := auth.AuthStores{
		Users:       txStore,
		Logins:      txStore,
		Sessions:    txStore,
		Credentials: txStore,
	}
	rollback := func() { _ = tx.Rollback() }
	return stores, tx.Commit, rollback, nil
}

// Ensure OIDCStore satisfies auth.Transactioner at compile time.
var _ auth.Transactioner = (*OIDCStore)(nil)

// Ensure OIDCStore satisfies all store interfaces at compile time.
var (
	_ auth.UserStore            = (*OIDCStore)(nil)
	_ auth.LoginIdentityStore   = (*OIDCStore)(nil)
	_ auth.ChannelIdentityStore = (*OIDCStore)(nil)
	_ auth.SessionStore         = (*OIDCStore)(nil)
	_ auth.CredentialStore      = (*OIDCStore)(nil)
	_ auth.OIDCCodeStore        = (*OIDCStore)(nil)
	_ auth.OIDCAccessTokenStore = (*OIDCStore)(nil)
)

// ---- Agent assignments ----

func (s *OIDCStore) AssignAgent(ctx context.Context, userID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO auth_user_agent (user_id, agent_id) VALUES (?, ?)`,
		userID, agentID)
	if err != nil {
		return fmt.Errorf("assign agent %q to user %s: %w", agentID, userID, err)
	}
	return nil
}

func (s *OIDCStore) RemoveAgent(ctx context.Context, userID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_user_agent WHERE user_id=? AND agent_id=?`,
		userID, agentID)
	if err != nil {
		return fmt.Errorf("remove agent %q from user %s: %w", agentID, userID, err)
	}
	return nil
}

func (s *OIDCStore) ListUserAgentIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id FROM auth_user_agent WHERE user_id=? ORDER BY agent_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user agents for user %s: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id FROM auth_user_agent WHERE agent_id=? ORDER BY user_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent users for agent %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()
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

func parseOIDCTime(s string) time.Time {
	t, _ := time.ParseInLocation(oidcTimeLayout, s, time.UTC)
	return t
}

// ---- UserStore ----

func (s *OIDCStore) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	if u.Role == "" {
		u.Role = auth.RoleUser
	}
	const q = `INSERT INTO auth_user (id, email, name, avatar_url, role, age_public_key, age_private_key)
	           VALUES (?, ?, ?, ?, ?, ?, '') RETURNING id, email, name, avatar_url, role, is_active,
	           default_agent_id, notify_identity_id, age_public_key, age_private_key,
	           created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, u.ID, u.Email, u.Name, u.AvatarURL, u.Role, u.AgePublicKey)
	return scanUser(row)
}

func (s *OIDCStore) GetUser(ctx context.Context, id string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanUser(row)
}

func (s *OIDCStore) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
	           age_public_key, age_private_key, created_at, updated_at
	           FROM auth_user WHERE email = ?`
	row := s.db.QueryRowContext(ctx, q, email)
	return scanUser(row)
}

func (s *OIDCStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	const q = `SELECT id, email, name, avatar_url, role, is_active, default_agent_id, notify_identity_id,
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

func (s *OIDCStore) ListActiveUserIDs(ctx context.Context) ([]string, error) {
	const q = `SELECT id FROM auth_user WHERE is_active = 1 ORDER BY id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

// GetVaultUser returns the age key fields for a user. Satisfies vault.DB.
func (s *OIDCStore) GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return sqlc.VaultUser{}, err
	}
	return sqlc.VaultUser{AgePublicKey: u.AgePublicKey, AgePrivateKey: u.AgePrivateKey}, nil
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

func (s *OIDCStore) UpdateUserRole(ctx context.Context, userID string, role string) error {
	const q = `UPDATE auth_user SET role = ?, updated_at = datetime('now') WHERE id = ?`
	_, err := s.db.ExecContext(ctx, q, role, userID)
	return err
}

func (s *OIDCStore) UpdateUserActive(ctx context.Context, userID string, isActive bool) error {
	active := 0
	if isActive {
		active = 1
	}
	const q = `UPDATE auth_user SET is_active = ?, updated_at = datetime('now') WHERE id = ?`
	_, err := s.db.ExecContext(ctx, q, active, userID)
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
	id, err := scanLoginIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.LoginIdentity{}, auth.ErrNotFound
	}
	return id, err
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
	const q = `INSERT INTO plugin_channel_identity (id, user_id, platform, external_id, name)
	           VALUES (?, ?, ?, ?, ?)
	           RETURNING id, user_id, platform, external_id, name, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, i.ID, i.UserID, i.Platform, i.ExternalID, i.Name)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentity(ctx context.Context, id string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM plugin_channel_identity WHERE id=?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM plugin_channel_identity WHERE platform=? AND external_id=?`
	row := s.db.QueryRowContext(ctx, q, platform, externalID)
	return scanChannelIdentity(row)
}

func (s *OIDCStore) ListChannelIdentitiesByUser(ctx context.Context, userID string) ([]auth.ChannelIdentity, error) {
	const q = `SELECT id, user_id, platform, external_id, name, created_at, updated_at
	           FROM plugin_channel_identity WHERE user_id=? ORDER BY created_at ASC`
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
	const q = `UPDATE plugin_channel_identity SET external_id=?, updated_at=datetime('now') WHERE id=?`
	_, err := s.db.ExecContext(ctx, q, externalID, id)
	return err
}

func (s *OIDCStore) DeleteChannelIdentity(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM plugin_channel_identity WHERE id=?`, id)
	return err
}

func scanChannelIdentity(r rowScanner) (auth.ChannelIdentity, error) {
	var ci auth.ChannelIdentity
	var createdAt, updatedAt string
	err := r.Scan(&ci.ID, &ci.UserID, &ci.Platform, &ci.ExternalID, &ci.Name, &createdAt, &updatedAt)
	if err != nil {
		return auth.ChannelIdentity{}, fmt.Errorf("plugin_channel_identity scan: %w", err)
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

func (s *OIDCStore) GetSession(ctx context.Context, id string) (auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at FROM auth_session WHERE id=?`
	return scanSession(s.db.QueryRowContext(ctx, q, id))
}

func (s *OIDCStore) ListSessionsByUser(ctx context.Context, userID string) ([]auth.Session, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, updated_at
	           FROM auth_session WHERE user_id=? AND expires_at > datetime('now')
	           ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

// ---- CredentialStore ----

func (s *OIDCStore) CreateCredential(ctx context.Context, c auth.Credential) (auth.Credential, error) {
	const q = `INSERT INTO auth_credential (id, user_id, password_hash)
	           VALUES (?, ?, ?) RETURNING id, user_id, password_hash, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, q, c.ID, c.UserID, c.PasswordHash)
	return scanCredential(row)
}

func (s *OIDCStore) GetCredentialByUserID(ctx context.Context, userID string) (auth.Credential, error) {
	const q = `SELECT id, user_id, password_hash, created_at, updated_at
	           FROM auth_credential WHERE user_id = ?`
	row := s.db.QueryRowContext(ctx, q, userID)
	c, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, err
}

func (s *OIDCStore) UpdateCredentialHash(ctx context.Context, userID, passwordHash string) error {
	const q = `UPDATE auth_credential SET password_hash = ?, updated_at = datetime('now') WHERE user_id = ?`
	res, err := s.db.ExecContext(ctx, q, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("auth_credential update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("auth_credential update: user %s not found", userID)
	}
	return nil
}

func (s *OIDCStore) DeleteCredential(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_credential WHERE user_id = ?`, userID)
	return err
}

func scanCredential(r rowScanner) (auth.Credential, error) {
	var c auth.Credential
	var createdAt, updatedAt string
	if err := r.Scan(&c.ID, &c.UserID, &c.PasswordHash, &createdAt, &updatedAt); err != nil {
		return auth.Credential{}, fmt.Errorf("auth_credential scan: %w", err)
	}
	c.CreatedAt = parseOIDCTime(createdAt)
	c.UpdatedAt = parseOIDCTime(updatedAt)
	return c, nil
}

// ---- OIDCCodeStore ----

func (s *OIDCStore) CreateOIDCCode(ctx context.Context, c auth.OIDCCode) (auth.OIDCCode, error) {
	scopesJSON, err := json.Marshal(c.Scopes)
	if err != nil {
		return auth.OIDCCode{}, fmt.Errorf("oidc_code: marshal scopes: %w", err)
	}
	const q = `INSERT INTO oidc_code
		(id, code_hash, user_id, client_id, redirect_uri, scopes, nonce,
		 pkce_challenge, pkce_method, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, code_hash, user_id, client_id, redirect_uri, scopes, nonce,
		          pkce_challenge, pkce_method, expires_at, consumed_at, created_at`
	row := s.db.QueryRowContext(ctx, q,
		c.ID, c.CodeHash, c.UserID, c.ClientID, c.RedirectURI,
		string(scopesJSON), c.Nonce, c.PKCEChallenge, c.PKCEMethod,
		c.ExpiresAt.UTC().Format(oidcTimeLayout),
	)
	return scanOIDCCode(row)
}

func (s *OIDCStore) ConsumeOIDCCode(ctx context.Context, codeHash string) (auth.OIDCCode, error) {
	now := time.Now().UTC().Format(oidcTimeLayout)
	const q = `UPDATE oidc_code SET consumed_at = ?
		WHERE code_hash = ? AND consumed_at IS NULL AND expires_at > ?
		RETURNING id, code_hash, user_id, client_id, redirect_uri, scopes, nonce,
		          pkce_challenge, pkce_method, expires_at, consumed_at, created_at`
	row := s.db.QueryRowContext(ctx, q, now, codeHash, now)
	code, err := scanOIDCCode(row)
	if errors.Is(err, sql.ErrNoRows) {
		// Distinguish "already consumed" from "not found" / "expired".
		var dummy string
		checkErr := s.db.QueryRowContext(ctx,
			`SELECT id FROM oidc_code WHERE code_hash = ?`, codeHash,
		).Scan(&dummy)
		if errors.Is(checkErr, sql.ErrNoRows) {
			return auth.OIDCCode{}, auth.ErrNotFound
		}
		// Row exists but was consumed or expired.
		var consumedAt *string
		var expiresAt string
		_ = s.db.QueryRowContext(ctx,
			`SELECT consumed_at, expires_at FROM oidc_code WHERE code_hash = ?`, codeHash,
		).Scan(&consumedAt, &expiresAt)
		if consumedAt != nil {
			return auth.OIDCCode{}, auth.ErrAlreadyConsumed
		}
		return auth.OIDCCode{}, auth.ErrExpired
	}
	return code, err
}

func scanOIDCCode(r rowScanner) (auth.OIDCCode, error) {
	var c auth.OIDCCode
	var scopesJSON, expiresAt, createdAt string
	var consumedAt *string
	if err := r.Scan(
		&c.ID, &c.CodeHash, &c.UserID, &c.ClientID, &c.RedirectURI,
		&scopesJSON, &c.Nonce, &c.PKCEChallenge, &c.PKCEMethod,
		&expiresAt, &consumedAt, &createdAt,
	); err != nil {
		return auth.OIDCCode{}, fmt.Errorf("oidc_code scan: %w", err)
	}
	if err := json.Unmarshal([]byte(scopesJSON), &c.Scopes); err != nil {
		return auth.OIDCCode{}, fmt.Errorf("oidc_code: unmarshal scopes: %w", err)
	}
	c.ExpiresAt = parseOIDCTime(expiresAt)
	c.CreatedAt = parseOIDCTime(createdAt)
	if consumedAt != nil {
		t := parseOIDCTime(*consumedAt)
		c.ConsumedAt = &t
	}
	return c, nil
}

// ---- OIDCAccessTokenStore ----

func (s *OIDCStore) CreateOIDCAccessToken(ctx context.Context, t auth.OIDCAccessToken) (auth.OIDCAccessToken, error) {
	scopesJSON, err := json.Marshal(t.Scopes)
	if err != nil {
		return auth.OIDCAccessToken{}, fmt.Errorf("oidc_access_token: marshal scopes: %w", err)
	}
	const q = `INSERT INTO oidc_access_token
		(id, token_hash, user_id, client_id, scopes, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, token_hash, user_id, client_id, scopes, expires_at, created_at`
	row := s.db.QueryRowContext(ctx, q,
		t.ID, t.TokenHash, t.UserID, t.ClientID,
		string(scopesJSON), t.ExpiresAt.UTC().Format(oidcTimeLayout),
	)
	return scanOIDCAccessToken(row)
}

func (s *OIDCStore) GetOIDCAccessTokenByHash(ctx context.Context, tokenHash string) (auth.OIDCAccessToken, error) {
	const q = `SELECT id, token_hash, user_id, client_id, scopes, expires_at, created_at
		FROM oidc_access_token WHERE token_hash = ?`
	row := s.db.QueryRowContext(ctx, q, tokenHash)
	t, err := scanOIDCAccessToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.OIDCAccessToken{}, auth.ErrNotFound
	}
	return t, err
}

func (s *OIDCStore) DeleteExpiredOIDCAccessTokens(ctx context.Context) error {
	now := time.Now().UTC().Format(oidcTimeLayout)
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_access_token WHERE expires_at <= ?`, now)
	return err
}

func scanOIDCAccessToken(r rowScanner) (auth.OIDCAccessToken, error) {
	var t auth.OIDCAccessToken
	var scopesJSON, expiresAt, createdAt string
	if err := r.Scan(
		&t.ID, &t.TokenHash, &t.UserID, &t.ClientID,
		&scopesJSON, &expiresAt, &createdAt,
	); err != nil {
		return auth.OIDCAccessToken{}, fmt.Errorf("oidc_access_token scan: %w", err)
	}
	if err := json.Unmarshal([]byte(scopesJSON), &t.Scopes); err != nil {
		return auth.OIDCAccessToken{}, fmt.Errorf("oidc_access_token: unmarshal scopes: %w", err)
	}
	t.ExpiresAt = parseOIDCTime(expiresAt)
	t.CreatedAt = parseOIDCTime(createdAt)
	return t, nil
}
