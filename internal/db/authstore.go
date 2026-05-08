package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const authTimeLayout = "2006-01-02 15:04:05"

// AuthStore implements auth.AuthStore using sqlc queries backed by SQLite.
type AuthStore struct {
	q *sqlc.Queries
}

// NewAuthStore creates a new AuthStore wrapping the given database connection.
func NewAuthStore(db *sql.DB) *AuthStore {
	return &AuthStore{q: sqlc.New(db)}
}

// --- Users ---

func (s *AuthStore) CreateUser(ctx context.Context, username, passwordHash string) (auth.AuthUser, error) {
	r, err := s.q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     username,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("create auth user %q: %w", username, err)
	}
	return userFromDB(r), nil
}

func (s *AuthStore) GetUser(ctx context.Context, id int64) (auth.AuthUser, error) {
	r, err := s.q.GetAuthUser(ctx, id)
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("get auth user %d: %w", id, err)
	}
	return userFromDB(r), nil
}

func (s *AuthStore) GetUserByUsername(ctx context.Context, username string) (auth.AuthUser, error) {
	r, err := s.q.GetAuthUserByUsername(ctx, username)
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("get auth user by username %q: %w", username, err)
	}
	return userFromDB(r), nil
}

func (s *AuthStore) ListUsers(ctx context.Context) ([]auth.AuthUser, error) {
	rows, err := s.q.ListAuthUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list auth users: %w", err)
	}
	out := make([]auth.AuthUser, len(rows))
	for i, r := range rows {
		out[i] = userFromDB(r)
	}
	return out, nil
}

func (s *AuthStore) UpdateUser(ctx context.Context, u auth.AuthUser) error {
	isActive := int64(0)
	if u.IsActive {
		isActive = 1
	}
	if err := s.q.UpdateAuthUser(ctx, sqlc.UpdateAuthUserParams{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		IsActive:     isActive,
	}); err != nil {
		return fmt.Errorf("update auth user %d: %w", u.ID, err)
	}
	return nil
}

func (s *AuthStore) UpdateUserDefaultAgent(ctx context.Context, userID int64, agentID string) error {
	if err := s.q.UpdateAuthUserDefaultAgent(ctx, sqlc.UpdateAuthUserDefaultAgentParams{
		DefaultAgentID: sql.NullString{String: agentID, Valid: agentID != ""},
		ID:             userID,
	}); err != nil {
		return fmt.Errorf("update default agent for user %d: %w", userID, err)
	}
	return nil
}

func (s *AuthStore) UpdateUserNotifyIdentity(ctx context.Context, userID int64, identityID *int64) error {
	var v sql.NullInt64
	if identityID != nil {
		v = sql.NullInt64{Int64: *identityID, Valid: true}
	}
	if err := s.q.UpdateAuthUserNotifyIdentity(ctx, sqlc.UpdateAuthUserNotifyIdentityParams{
		NotifyIdentityID: v,
		ID:               userID,
	}); err != nil {
		return fmt.Errorf("update notify identity for user %d: %w", userID, err)
	}
	return nil
}

func (s *AuthStore) DeleteUser(ctx context.Context, id int64) error {
	if err := s.q.DeleteAuthUser(ctx, id); err != nil {
		return fmt.Errorf("delete auth user %d: %w", id, err)
	}
	return nil
}

func (s *AuthStore) CountUsers(ctx context.Context) (int64, error) {
	return s.q.CountAuthUsers(ctx)
}

func (s *AuthStore) UpdateUserAgeKeys(ctx context.Context, userID int64, publicKey, privateKey string) error {
	if err := s.q.UpdateUserAgeKeys(ctx, sqlc.UpdateUserAgeKeysParams{
		AgePublicKey:  publicKey,
		AgePrivateKey: privateKey,
		ID:            userID,
	}); err != nil {
		return fmt.Errorf("update age keys for user %d: %w", userID, err)
	}
	return nil
}

func (s *AuthStore) UpdateUserRole(ctx context.Context, userID int64, role string) error {
	if err := s.q.UpdateAuthUserRole(ctx, sqlc.UpdateAuthUserRoleParams{
		Role: role,
		ID:   userID,
	}); err != nil {
		return fmt.Errorf("update role for user %d: %w", userID, err)
	}
	return nil
}

// --- Identities ---

func (s *AuthStore) CreateIdentity(ctx context.Context, i auth.Identity) (auth.Identity, error) {
	r, err := s.q.CreateAuthIdentity(ctx, sqlc.CreateAuthIdentityParams{
		UserID:     i.UserID,
		Platform:   i.Platform,
		ExternalID: i.ExternalID,
		Name:       i.Name,
	})
	if err != nil {
		return auth.Identity{}, fmt.Errorf("create identity: %w", err)
	}
	return identityFromDB(r), nil
}

func (s *AuthStore) GetIdentity(ctx context.Context, id int64) (auth.Identity, error) {
	r, err := s.q.GetAuthIdentity(ctx, id)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("get identity %d: %w", id, err)
	}
	return identityFromDB(r), nil
}

func (s *AuthStore) GetIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.Identity, error) {
	r, err := s.q.GetAuthIdentityByPlatform(ctx, sqlc.GetAuthIdentityByPlatformParams{
		Platform:   platform,
		ExternalID: externalID,
	})
	if err != nil {
		return auth.Identity{}, fmt.Errorf("get identity by platform %s/%s: %w", platform, externalID, err)
	}
	return identityFromDB(r), nil
}

func (s *AuthStore) UpdateIdentityExternalID(ctx context.Context, id int64, externalID string) error {
	if err := s.q.UpdateAuthIdentityExternalID(ctx, sqlc.UpdateAuthIdentityExternalIDParams{
		ExternalID: externalID,
		ID:         id,
	}); err != nil {
		return fmt.Errorf("update identity %d external_id: %w", id, err)
	}
	return nil
}

func (s *AuthStore) ListIdentitiesByUser(ctx context.Context, userID int64) ([]auth.Identity, error) {
	rows, err := s.q.ListAuthIdentitiesByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities for user %d: %w", userID, err)
	}
	out := make([]auth.Identity, len(rows))
	for i, r := range rows {
		out[i] = identityFromDB(r)
	}
	return out, nil
}

func (s *AuthStore) DeleteIdentity(ctx context.Context, id int64) error {
	if err := s.q.DeleteAuthIdentity(ctx, id); err != nil {
		return fmt.Errorf("delete identity %d: %w", id, err)
	}
	return nil
}

// --- Policies ---

func (s *AuthStore) CreatePolicy(ctx context.Context, p auth.Policy) (auth.Policy, error) {
	isSystem := int64(0)
	if p.IsSystem {
		isSystem = 1
	}
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	r, err := s.q.CreateAuthPolicy(ctx, sqlc.CreateAuthPolicyParams{
		ID:         p.ID,
		Name:       p.Name,
		Effect:     p.Effect,
		Subjects:   p.Subjects,
		Actions:    p.Actions,
		Resources:  p.Resources,
		Conditions: p.Conditions,
		Priority:   int64(p.Priority),
		IsSystem:   isSystem,
		Enabled:    enabled,
	})
	if err != nil {
		return auth.Policy{}, fmt.Errorf("create policy %q: %w", p.ID, err)
	}
	return policyFromDB(r), nil
}

func (s *AuthStore) GetPolicy(ctx context.Context, id string) (auth.Policy, error) {
	r, err := s.q.GetAuthPolicy(ctx, id)
	if err != nil {
		return auth.Policy{}, fmt.Errorf("get policy %q: %w", id, err)
	}
	return policyFromDB(r), nil
}

func (s *AuthStore) ListPolicies(ctx context.Context) ([]auth.Policy, error) {
	rows, err := s.q.ListAuthPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	out := make([]auth.Policy, len(rows))
	for i, r := range rows {
		out[i] = policyFromDB(r)
	}
	return out, nil
}

func (s *AuthStore) ListEnabledPolicies(ctx context.Context) ([]auth.Policy, error) {
	rows, err := s.q.ListEnabledAuthPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled policies: %w", err)
	}
	out := make([]auth.Policy, len(rows))
	for i, r := range rows {
		out[i] = policyFromDB(r)
	}
	return out, nil
}

func (s *AuthStore) UpdatePolicy(ctx context.Context, p auth.Policy) error {
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	if err := s.q.UpdateAuthPolicy(ctx, sqlc.UpdateAuthPolicyParams{
		ID:         p.ID,
		Name:       p.Name,
		Effect:     p.Effect,
		Subjects:   p.Subjects,
		Actions:    p.Actions,
		Resources:  p.Resources,
		Conditions: p.Conditions,
		Priority:   int64(p.Priority),
		Enabled:    enabled,
	}); err != nil {
		return fmt.Errorf("update policy %q: %w", p.ID, err)
	}
	return nil
}

func (s *AuthStore) DeletePolicy(ctx context.Context, id string) error {
	if err := s.q.DeleteAuthPolicy(ctx, id); err != nil {
		return fmt.Errorf("delete policy %q: %w", id, err)
	}
	return nil
}

// --- User-Agent assignments ---

func (s *AuthStore) AssignAgent(ctx context.Context, userID int64, agentID string) error {
	if err := s.q.AssignUserAgent(ctx, sqlc.AssignUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("assign agent %q to user %d: %w", agentID, userID, err)
	}
	return nil
}

func (s *AuthStore) RemoveAgent(ctx context.Context, userID int64, agentID string) error {
	if err := s.q.RemoveUserAgent(ctx, sqlc.RemoveUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("remove agent %q from user %d: %w", agentID, userID, err)
	}
	return nil
}

func (s *AuthStore) ListUserAgentIDs(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.q.ListUserAgents(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user agents for user %d: %w", userID, err)
	}
	out := make([]string, len(rows))
	copy(out, rows)
	return out, nil
}

func (s *AuthStore) ListAgentUserIDs(ctx context.Context, agentID string) ([]int64, error) {
	rows, err := s.q.ListAgentUsers(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent users for agent %q: %w", agentID, err)
	}
	out := make([]int64, len(rows))
	copy(out, rows)
	return out, nil
}

// --- Sessions ---

func (s *AuthStore) CreateSession(ctx context.Context, sess auth.Session) (auth.Session, error) {
	r, err := s.q.CreateAuthSession(ctx, sqlc.CreateAuthSessionParams{
		ID:        sess.ID,
		UserID:    sess.UserID,
		ExpiresAt: sess.ExpiresAt.UTC().Format(authTimeLayout),
	})
	if err != nil {
		return auth.Session{}, fmt.Errorf("create session: %w", err)
	}
	return sessionFromDB(r), nil
}

func (s *AuthStore) GetSession(ctx context.Context, id string) (auth.Session, error) {
	r, err := s.q.GetAuthSession(ctx, id)
	if err != nil {
		return auth.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	return sessionFromDB(r), nil
}

func (s *AuthStore) DeleteSession(ctx context.Context, id string) error {
	if err := s.q.DeleteAuthSession(ctx, id); err != nil {
		return fmt.Errorf("delete session %q: %w", id, err)
	}
	return nil
}

func (s *AuthStore) DeleteExpiredSessions(ctx context.Context) error {
	if err := s.q.DeleteExpiredAuthSessions(ctx); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func (s *AuthStore) DeleteUserSessions(ctx context.Context, userID int64) error {
	if err := s.q.DeleteUserAuthSessions(ctx, userID); err != nil {
		return fmt.Errorf("delete sessions for user %d: %w", userID, err)
	}
	return nil
}

func (s *AuthStore) UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error {
	if err := s.q.UpdateAuthSessionExpiry(ctx, sqlc.UpdateAuthSessionExpiryParams{
		ID:        id,
		ExpiresAt: expiresAt.UTC().Format(authTimeLayout),
	}); err != nil {
		return fmt.Errorf("update session expiry %q: %w", id, err)
	}
	return nil
}

// --- User tokens ---

func (s *AuthStore) CreateUserToken(ctx context.Context, token auth.UserToken) (auth.UserToken, error) {
	autoGenerated := int64(0)
	if token.AutoGenerated {
		autoGenerated = 1
	}
	r, err := s.q.CreateAuthUserToken(ctx, sqlc.CreateAuthUserTokenParams{
		UserID:        token.UserID,
		Name:          token.Name,
		TokenHash:     token.TokenHash,
		TokenPrefix:   token.TokenPrefix,
		AutoGenerated: autoGenerated,
		ExpiresAt:     nullStringFromTimePtr(token.ExpiresAt),
	})
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("create user token for user %d: %w", token.UserID, err)
	}
	return userTokenFromDB(r), nil
}

func (s *AuthStore) GetUserTokenByHash(ctx context.Context, tokenHash string) (auth.UserToken, error) {
	r, err := s.q.GetAuthUserTokenByHash(ctx, tokenHash)
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("get user token by hash: %w", err)
	}
	return userTokenFromDB(r), nil
}

func (s *AuthStore) GetActiveUserTokenByHash(ctx context.Context, tokenHash string) (auth.UserToken, error) {
	r, err := s.q.GetActiveAuthUserTokenByHash(ctx, tokenHash)
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("get active user token by hash: %w", err)
	}
	return userTokenFromDB(r), nil
}

func (s *AuthStore) GetActiveAutoUserToken(ctx context.Context, userID int64) (auth.UserToken, error) {
	r, err := s.q.GetActiveAutoAuthUserTokenByUser(ctx, userID)
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("get active auto user token for user %d: %w", userID, err)
	}
	return userTokenFromDB(r), nil
}

func (s *AuthStore) RotateUserToken(ctx context.Context, id int64) (int64, error) {
	rows, err := s.q.RotateAuthUserToken(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("rotate user token %d: %w", id, err)
	}
	return rows, nil
}

func (s *AuthStore) RevokeUserToken(ctx context.Context, id int64) (int64, error) {
	rows, err := s.q.RevokeAuthUserToken(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("revoke user token %d: %w", id, err)
	}
	return rows, nil
}

func (s *AuthStore) UpdateUserTokenLastUsed(ctx context.Context, id int64) (int64, error) {
	rows, err := s.q.UpdateAuthUserTokenLastUsed(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("update user token %d last used: %w", id, err)
	}
	return rows, nil
}

// --- Helpers ---

func parseAuthTime(s string) time.Time {
	t, err := time.Parse(authTimeLayout, s)
	if err != nil {
		slog.Warn("db: failed to parse auth time", "value", s, "error", err)
	}
	return t
}

func userFromDB(r sqlc.AuthUser) auth.AuthUser {
	u := auth.AuthUser{
		ID:           r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
		Role:         r.Role,
		IsActive:     r.IsActive == 1,
		CreatedAt:    parseAuthTime(r.CreatedAt),
		UpdatedAt:    parseAuthTime(r.UpdatedAt),
	}
	if r.DefaultAgentID.Valid {
		u.DefaultAgentID = r.DefaultAgentID.String
	}
	if r.NotifyIdentityID.Valid {
		id := r.NotifyIdentityID.Int64
		u.NotifyIdentityID = &id
	}
	return u
}

func identityFromDB(r sqlc.AuthIdentity) auth.Identity {
	return auth.Identity{
		ID:         r.ID,
		UserID:     r.UserID,
		Platform:   r.Platform,
		ExternalID: r.ExternalID,
		Name:       r.Name,
		LinkedAt:   parseAuthTime(r.LinkedAt),
	}
}

func policyFromDB(r sqlc.AuthPolicy) auth.Policy {
	return auth.Policy{
		ID:         r.ID,
		Name:       r.Name,
		Effect:     r.Effect,
		Subjects:   r.Subjects,
		Actions:    r.Actions,
		Resources:  r.Resources,
		Conditions: r.Conditions,
		Priority:   int(r.Priority),
		IsSystem:   r.IsSystem == 1,
		Enabled:    r.Enabled == 1,
		CreatedAt:  parseAuthTime(r.CreatedAt),
	}
}

func sessionFromDB(r sqlc.AuthSession) auth.Session {
	return auth.Session{
		ID:        r.ID,
		UserID:    r.UserID,
		ExpiresAt: parseAuthTime(r.ExpiresAt),
		CreatedAt: parseAuthTime(r.CreatedAt),
	}
}

func userTokenFromDB(r sqlc.AuthUserToken) auth.UserToken {
	return auth.UserToken{
		ID:            r.ID,
		UserID:        r.UserID,
		Name:          r.Name,
		TokenHash:     r.TokenHash,
		TokenPrefix:   r.TokenPrefix,
		AutoGenerated: r.AutoGenerated == 1,
		LastUsedAt:    timePtrFromNullString(r.LastUsedAt),
		ExpiresAt:     timePtrFromNullString(r.ExpiresAt),
		RotatedAt:     timePtrFromNullString(r.RotatedAt),
		RevokedAt:     timePtrFromNullString(r.RevokedAt),
		CreatedAt:     parseAuthTime(r.CreatedAt),
		UpdatedAt:     parseAuthTime(r.UpdatedAt),
	}
}

func nullStringFromTimePtr(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(authTimeLayout), Valid: true}
}

func timePtrFromNullString(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseAuthTime(s.String)
	return &t
}
