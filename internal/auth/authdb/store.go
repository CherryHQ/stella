package authdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/db/sqlc"
)

const timeLayout = "2006-01-02 15:04:05"

// Store implements auth.AuthStore using sqlc queries backed by SQLite.
type Store struct {
	q *sqlc.Queries
}

// New creates a new authdb.Store wrapping the given database connection.
func New(db *sql.DB) *Store {
	return &Store{q: sqlc.New(db)}
}

// --- Users ---

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (auth.AuthUser, error) {
	r, err := s.q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     username,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("create auth user %q: %w", username, err)
	}
	return userFromDB(r), nil
}

func (s *Store) GetUser(ctx context.Context, id int64) (auth.AuthUser, error) {
	r, err := s.q.GetAuthUser(ctx, id)
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("get auth user %d: %w", id, err)
	}
	return userFromDB(r), nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (auth.AuthUser, error) {
	r, err := s.q.GetAuthUserByUsername(ctx, username)
	if err != nil {
		return auth.AuthUser{}, fmt.Errorf("get auth user by username %q: %w", username, err)
	}
	return userFromDB(r), nil
}

func (s *Store) ListUsers(ctx context.Context) ([]auth.AuthUser, error) {
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

func (s *Store) UpdateUser(ctx context.Context, u auth.AuthUser) error {
	isActive := int64(0)
	if u.IsActive {
		isActive = 1
	}
	return s.q.UpdateAuthUser(ctx, sqlc.UpdateAuthUserParams{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		IsActive:     isActive,
	})
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.q.DeleteAuthUser(ctx, id)
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	return s.q.CountAuthUsers(ctx)
}

// --- Roles ---

func (s *Store) CreateRole(ctx context.Context, r auth.Role) (auth.Role, error) {
	isSystem := int64(0)
	if r.IsSystem {
		isSystem = 1
	}
	row, err := s.q.CreateAuthRole(ctx, sqlc.CreateAuthRoleParams{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		IsSystem:    isSystem,
	})
	if err != nil {
		return auth.Role{}, fmt.Errorf("create auth role %q: %w", r.ID, err)
	}
	return roleFromDB(row), nil
}

func (s *Store) GetRole(ctx context.Context, id string) (auth.Role, error) {
	r, err := s.q.GetAuthRole(ctx, id)
	if err != nil {
		return auth.Role{}, fmt.Errorf("get auth role %q: %w", id, err)
	}
	return roleFromDB(r), nil
}

func (s *Store) ListRoles(ctx context.Context) ([]auth.Role, error) {
	rows, err := s.q.ListAuthRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list auth roles: %w", err)
	}
	out := make([]auth.Role, len(rows))
	for i, r := range rows {
		out[i] = roleFromDB(r)
	}
	return out, nil
}

func (s *Store) UpdateRole(ctx context.Context, r auth.Role) error {
	return s.q.UpdateAuthRole(ctx, sqlc.UpdateAuthRoleParams{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
	})
}

func (s *Store) DeleteRole(ctx context.Context, id string) error {
	return s.q.DeleteAuthRole(ctx, id)
}

// --- User-Role assignments ---

func (s *Store) AssignRole(ctx context.Context, userID int64, roleID string) error {
	return s.q.AssignUserRole(ctx, sqlc.AssignUserRoleParams{
		UserID: userID,
		RoleID: roleID,
	})
}

func (s *Store) RemoveRole(ctx context.Context, userID int64, roleID string) error {
	return s.q.RemoveUserRole(ctx, sqlc.RemoveUserRoleParams{
		UserID: userID,
		RoleID: roleID,
	})
}

func (s *Store) ListUserRoles(ctx context.Context, userID int64) ([]auth.Role, error) {
	rows, err := s.q.ListUserRoles(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user roles for user %d: %w", userID, err)
	}
	out := make([]auth.Role, len(rows))
	for i, r := range rows {
		out[i] = roleFromDB(r)
	}
	return out, nil
}

// --- Identities ---

func (s *Store) CreateIdentity(ctx context.Context, i auth.Identity) (auth.Identity, error) {
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

func (s *Store) GetIdentity(ctx context.Context, id int64) (auth.Identity, error) {
	r, err := s.q.GetAuthIdentity(ctx, id)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("get identity %d: %w", id, err)
	}
	return identityFromDB(r), nil
}

func (s *Store) GetIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.Identity, error) {
	r, err := s.q.GetAuthIdentityByPlatform(ctx, sqlc.GetAuthIdentityByPlatformParams{
		Platform:   platform,
		ExternalID: externalID,
	})
	if err != nil {
		return auth.Identity{}, fmt.Errorf("get identity by platform %s/%s: %w", platform, externalID, err)
	}
	return identityFromDB(r), nil
}

func (s *Store) ListIdentitiesByUser(ctx context.Context, userID int64) ([]auth.Identity, error) {
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

func (s *Store) DeleteIdentity(ctx context.Context, id int64) error {
	return s.q.DeleteAuthIdentity(ctx, id)
}

// --- Policies ---

func (s *Store) CreatePolicy(ctx context.Context, p auth.Policy) (auth.Policy, error) {
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

func (s *Store) GetPolicy(ctx context.Context, id string) (auth.Policy, error) {
	r, err := s.q.GetAuthPolicy(ctx, id)
	if err != nil {
		return auth.Policy{}, fmt.Errorf("get policy %q: %w", id, err)
	}
	return policyFromDB(r), nil
}

func (s *Store) ListPolicies(ctx context.Context) ([]auth.Policy, error) {
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

func (s *Store) ListEnabledPolicies(ctx context.Context) ([]auth.Policy, error) {
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

func (s *Store) UpdatePolicy(ctx context.Context, p auth.Policy) error {
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	return s.q.UpdateAuthPolicy(ctx, sqlc.UpdateAuthPolicyParams{
		ID:         p.ID,
		Name:       p.Name,
		Effect:     p.Effect,
		Subjects:   p.Subjects,
		Actions:    p.Actions,
		Resources:  p.Resources,
		Conditions: p.Conditions,
		Priority:   int64(p.Priority),
		Enabled:    enabled,
	})
}

func (s *Store) DeletePolicy(ctx context.Context, id string) error {
	return s.q.DeleteAuthPolicy(ctx, id)
}

// --- User-Agent assignments ---

func (s *Store) AssignAgent(ctx context.Context, userID int64, agentID string) error {
	return s.q.AssignUserAgent(ctx, sqlc.AssignUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	})
}

func (s *Store) RemoveAgent(ctx context.Context, userID int64, agentID string) error {
	return s.q.RemoveUserAgent(ctx, sqlc.RemoveUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	})
}

func (s *Store) ListUserAgentIDs(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.q.ListUserAgents(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user agents for user %d: %w", userID, err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, nil
}

func (s *Store) ListAgentUserIDs(ctx context.Context, agentID string) ([]int64, error) {
	rows, err := s.q.ListAgentUsers(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent users for agent %q: %w", agentID, err)
	}
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, nil
}

// --- Sessions ---

func (s *Store) CreateSession(ctx context.Context, sess auth.Session) (auth.Session, error) {
	r, err := s.q.CreateAuthSession(ctx, sqlc.CreateAuthSessionParams{
		ID:        sess.ID,
		UserID:    sess.UserID,
		ExpiresAt: sess.ExpiresAt.UTC().Format(timeLayout),
	})
	if err != nil {
		return auth.Session{}, fmt.Errorf("create session: %w", err)
	}
	return sessionFromDB(r), nil
}

func (s *Store) GetSession(ctx context.Context, id string) (auth.Session, error) {
	r, err := s.q.GetAuthSession(ctx, id)
	if err != nil {
		return auth.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	return sessionFromDB(r), nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.q.DeleteAuthSession(ctx, id)
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	return s.q.DeleteExpiredAuthSessions(ctx)
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	return s.q.DeleteUserAuthSessions(ctx, userID)
}

func (s *Store) UpdateSessionExpiry(ctx context.Context, id string, expiresAt string) error {
	return s.q.UpdateAuthSessionExpiry(ctx, sqlc.UpdateAuthSessionExpiryParams{
		ID:        id,
		ExpiresAt: expiresAt,
	})
}

// --- Helpers ---

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeLayout, s)
	return t
}

func userFromDB(r sqlc.AuthUser) auth.AuthUser {
	return auth.AuthUser{
		ID:           r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
		IsActive:     r.IsActive == 1,
		CreatedAt:    parseTime(r.CreatedAt),
		UpdatedAt:    parseTime(r.UpdatedAt),
	}
}

func roleFromDB(r sqlc.AuthRole) auth.Role {
	return auth.Role{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		IsSystem:    r.IsSystem == 1,
		CreatedAt:   parseTime(r.CreatedAt),
	}
}

func identityFromDB(r sqlc.AuthIdentity) auth.Identity {
	return auth.Identity{
		ID:         r.ID,
		UserID:     r.UserID,
		Platform:   r.Platform,
		ExternalID: r.ExternalID,
		Name:       r.Name,
		LinkedAt:   parseTime(r.LinkedAt),
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
		CreatedAt:  parseTime(r.CreatedAt),
	}
}

func sessionFromDB(r sqlc.AuthSession) auth.Session {
	return auth.Session{
		ID:        r.ID,
		UserID:    r.UserID,
		ExpiresAt: parseTime(r.ExpiresAt),
		CreatedAt: parseTime(r.CreatedAt),
	}
}
