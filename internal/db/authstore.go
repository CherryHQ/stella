package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/orgctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func authRequireOrgID(ctx context.Context) (string, error) {
	orgID := orgctx.OrgIDFromContext(ctx)
	if orgID == "" {
		return "", fmt.Errorf("org_id not set in context")
	}
	return orgID, nil
}

const authTimeLayout = "2006-01-02 15:04:05"

// AuthStore implements auth.AuthStore using sqlc queries backed by SQLite.
// It embeds OIDCStore to satisfy all new auth store interfaces so that
// gateway.go can pass a single *AuthStore to all wiring points without
// knowing about the split between sqlc-backed and raw-SQL-backed stores.
type AuthStore struct {
	*OIDCStore
	q *sqlc.Queries
}

// NewAuthStore creates a new AuthStore wrapping the given database connection.
func NewAuthStore(db *sql.DB) *AuthStore {
	return &AuthStore{OIDCStore: NewOIDCStore(db), q: sqlc.New(db)}
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
		OrgID:      p.OrgID,
	})
	if err != nil {
		return auth.Policy{}, fmt.Errorf("create policy %q: %w", p.ID, err)
	}
	return policyFromDB(r), nil
}

func (s *AuthStore) GetPolicy(ctx context.Context, id string) (auth.Policy, error) {
	orgID, err := authRequireOrgID(ctx)
	if err != nil {
		return auth.Policy{}, err
	}
	r, err := s.q.GetAuthPolicy(ctx, sqlc.GetAuthPolicyParams{ID: id, OrgID: orgID})
	if err != nil {
		return auth.Policy{}, fmt.Errorf("get policy %q: %w", id, err)
	}
	return policyFromDB(r), nil
}

func (s *AuthStore) ListPolicies(ctx context.Context) ([]auth.Policy, error) {
	orgID, err := authRequireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAuthPolicies(ctx, orgID)
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
	orgID, err := authRequireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnabledAuthPolicies(ctx, orgID)
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
	orgID, err := authRequireOrgID(ctx)
	if err != nil {
		return err
	}
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
		OrgID:      orgID,
	}); err != nil {
		return fmt.Errorf("update policy %q: %w", p.ID, err)
	}
	return nil
}

func (s *AuthStore) DeletePolicy(ctx context.Context, id string) error {
	orgID, err := authRequireOrgID(ctx)
	if err != nil {
		return err
	}
	if err := s.q.DeleteAuthPolicy(ctx, sqlc.DeleteAuthPolicyParams{ID: id, OrgID: orgID}); err != nil {
		return fmt.Errorf("delete policy %q: %w", id, err)
	}
	return nil
}

// --- User-Agent assignments ---

func (s *AuthStore) AssignAgent(ctx context.Context, userID string, agentID string) error {
	if err := s.q.AssignUserAgent(ctx, sqlc.AssignUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("assign agent %q to user %s: %w", agentID, userID, err)
	}
	return nil
}

func (s *AuthStore) RemoveAgent(ctx context.Context, userID string, agentID string) error {
	if err := s.q.RemoveUserAgent(ctx, sqlc.RemoveUserAgentParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("remove agent %q from user %s: %w", agentID, userID, err)
	}
	return nil
}

func (s *AuthStore) ListUserAgentIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.q.ListUserAgents(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user agents for user %s: %w", userID, err)
	}
	out := make([]string, len(rows))
	copy(out, rows)
	return out, nil
}

func (s *AuthStore) ListAgentUserIDs(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.q.ListAgentUsers(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent users for agent %q: %w", agentID, err)
	}
	out := make([]string, len(rows))
	copy(out, rows)
	return out, nil
}

// --- User tokens ---

func (s *AuthStore) CreateUserToken(ctx context.Context, token auth.UserToken) (auth.UserToken, error) {
	autoGenerated := int64(0)
	if token.AutoGenerated {
		autoGenerated = 1
	}
	r, err := s.q.CreateAuthUserToken(ctx, sqlc.CreateAuthUserTokenParams{
		ID:            uuid.NewString(),
		UserID:        token.UserID,
		Name:          token.Name,
		TokenHash:     token.TokenHash,
		TokenPrefix:   token.TokenPrefix,
		AutoGenerated: autoGenerated,
		ExpiresAt:     nullStringFromTimePtr(token.ExpiresAt),
	})
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("create user token for user %s: %w", token.UserID, err)
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

func (s *AuthStore) GetActiveAutoUserToken(ctx context.Context, userID string) (auth.UserToken, error) {
	r, err := s.q.GetActiveAutoAuthUserTokenByUser(ctx, userID)
	if err != nil {
		return auth.UserToken{}, fmt.Errorf("get active auto user token for user %s: %w", userID, err)
	}
	return userTokenFromDB(r), nil
}

func (s *AuthStore) RotateUserToken(ctx context.Context, id string) (int64, error) {
	rows, err := s.q.RotateAuthUserToken(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("rotate user token %s: %w", id, err)
	}
	return rows, nil
}

func (s *AuthStore) RevokeUserToken(ctx context.Context, id string) (int64, error) {
	rows, err := s.q.RevokeAuthUserToken(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("revoke user token %s: %w", id, err)
	}
	return rows, nil
}

func (s *AuthStore) UpdateUserTokenLastUsed(ctx context.Context, id string) (int64, error) {
	rows, err := s.q.UpdateAuthUserTokenLastUsed(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("update user token %s last used: %w", id, err)
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
		OrgID:      r.OrgID,
		CreatedAt:  parseAuthTime(r.CreatedAt),
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
