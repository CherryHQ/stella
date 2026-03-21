package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// builtinPolicies defines the system policies seeded on bootstrap.
var builtinPolicies = []Policy{
	{
		ID:         "system:admin-full-access",
		Name:       "Admin Full Access",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["admin"]}`,
		Actions:    `["*"]`,
		Resources:  `["*"]`,
		Conditions: `{}`,
		Priority:   100,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-system-agents",
		Name:       "User System Agents",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","execute"]`,
		Resources:  `["agent"]`,
		Conditions: `{"resource.scope":{"eq":"system"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-assigned-agents",
		Name:       "User Assigned Agents",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","execute"]`,
		Resources:  `["agent"]`,
		Conditions: `{"resource.id":{"in":"subject.agent_ids"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-sessions",
		Name:       "User Own Sessions",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["session"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-data",
		Name:       "User Own Data",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write"]`,
		Resources:  `["user_data"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-skills",
		Name:       "User Own Skills",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["skill"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-profile",
		Name:       "User Own Profile",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write"]`,
		Resources:  `["user"]`,
		Conditions: `{"resource.id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-view-agents-list",
		Name:       "User View Agents List",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read"]`,
		Resources:  `["agent_list"]`,
		Conditions: `{}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-scheduler",
		Name:       "User Own Scheduler Jobs",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["scheduler"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
}

// SeedPolicies ensures the built-in policies exist in the store.
// It uses an idempotent pattern: existing entries are skipped.
func SeedPolicies(ctx context.Context, store AuthStore) error {
	for _, policy := range builtinPolicies {
		if _, err := store.CreatePolicy(ctx, policy); err != nil {
			if isAlreadyExists(err) {
				slog.Debug("auth seed: policy already exists", "policy_id", policy.ID)
				continue
			}
			return fmt.Errorf("seed policy %q: %w", policy.ID, err)
		}
		slog.Info("auth seed: created policy", "policy_id", policy.ID)
	}

	return nil
}

// isAlreadyExists checks whether the error indicates a unique constraint
// violation (SQLite UNIQUE constraint failed).
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	// sql.ErrNoRows is not a constraint error, but check common patterns.
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	// SQLite returns "UNIQUE constraint failed" for duplicate keys.
	errStr := err.Error()
	return strings.Contains(errStr, "UNIQUE constraint failed") ||
		strings.Contains(errStr, "constraint failed") ||
		strings.Contains(errStr, "duplicate key")
}
