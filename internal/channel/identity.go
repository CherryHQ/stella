package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

// ErrAgentAccessDenied is returned when a user tries to use an agent they
// don't have access to.
var ErrAgentAccessDenied = errors.New("you don't have access to this agent, contact an admin")

// ChatContext describes the chat environment for agent routing.
type ChatContext struct {
	Platform string // "telegram", "qq", "feishu", "cli"
	ChatID   string // group/channel ID (empty for DMs)
	IsGroup  bool
}

// ResolvedIdentity extends config.User with auth system information.
// When AuthUserID > 0, the user has been resolved via auth_identities.
type ResolvedIdentity struct {
	config.User
	AuthUserID int64    // auth_users.id (0 if not resolved via auth)
	Roles      []string // role IDs from auth_user_roles
}

// ResolveUser upserts a user by external ID + platform, returning the user record.
// This is the legacy path that only uses settings_users.
func ResolveUser(ctx context.Context, store config.Store, externalID, platform, name string) (config.User, error) {
	user, err := store.UpsertUser(ctx, externalID, platform, name)
	if err != nil {
		return config.User{}, fmt.Errorf("resolve user: %w", err)
	}
	return user, nil
}

// ResolveUserWithAuth resolves a channel user with the auth system.
// Resolution order:
//  1. Look up auth_identities for (platform, externalID)
//  2. If found: resolve to auth_user, also upsert settings_users for compatibility
//  3. If not found: fallback to settings_users, auto-migrate to auth system
//
// Auto-migration: when a settings_users record exists but no auth_identity,
// creates an auth_user (username="{platform}_{externalID}", random password)
// with the "user" role, and links the identity.
func ResolveUserWithAuth(ctx context.Context, store config.Store, authStore auth.AuthStore, externalID, platform, name string) (ResolvedIdentity, error) {
	log := slog.With("component", "identity", "platform", platform, "external_id", externalID)

	// Always upsert in settings_users for backward compat (sessions, memories, etc.)
	user, err := store.UpsertUser(ctx, externalID, platform, name)
	if err != nil {
		return ResolvedIdentity{}, fmt.Errorf("resolve user: %w", err)
	}

	// Try auth_identities first.
	identity, err := authStore.GetIdentityByPlatform(ctx, platform, externalID)
	if err == nil {
		// Found linked identity — resolve the auth user.
		authUser, err := authStore.GetUser(ctx, identity.UserID)
		if err != nil {
			log.Error("auth user not found for linked identity", "user_id", identity.UserID, "error", err)
			return ResolvedIdentity{User: user}, nil
		}
		if !authUser.IsActive {
			return ResolvedIdentity{}, fmt.Errorf("account is deactivated")
		}
		roles, _ := authStore.ListUserRoles(ctx, authUser.ID)
		roleIDs := make([]string, len(roles))
		for i, r := range roles {
			roleIDs[i] = r.ID
		}
		return ResolvedIdentity{
			User:       user,
			AuthUserID: authUser.ID,
			Roles:      roleIDs,
		}, nil
	}

	// No linked identity — return the legacy settings_users record without
	// auth resolution. The user must link their account via a link code.
	log.Debug("no linked identity found, user must link via link code")
	return ResolvedIdentity{User: user}, nil
}

// ResolveAgent determines which agent to route to.
// DM: user's default_agent_id
// Group: chat_agents(platform, chat_id)
// Fallback: first enabled agent
func ResolveAgent(ctx context.Context, store config.Store, user config.User, chat ChatContext) (string, error) {
	// Group chat: look up per-group agent assignment.
	if chat.IsGroup && chat.ChatID != "" {
		agentID, err := store.GetChatAgent(ctx, chat.Platform, chat.ChatID)
		if err == nil && agentID != "" {
			return agentID, nil
		}
	}

	// DM: use user's default agent.
	if !chat.IsGroup && user.DefaultAgentID != "" {
		return user.DefaultAgentID, nil
	}

	// Fallback: first enabled agent.
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve agent: list enabled agents: %w", err)
	}
	if len(agents) == 0 {
		return "", fmt.Errorf("resolve agent: no enabled agents found")
	}
	return agents[0].ID, nil
}

// ResolveAgentWithAuth determines which agent to route to, checking access
// via the policy engine. Returns ErrAgentAccessDenied if the user cannot
// access the resolved agent.
func ResolveAgentWithAuth(ctx context.Context, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, user config.User, identity ResolvedIdentity, chat ChatContext) (string, error) {
	log := slog.With("component", "identity", "auth_user_id", identity.AuthUserID)

	// Build subject for policy checks.
	assignedIDs, _ := authStore.ListUserAgentIDs(ctx, identity.AuthUserID)
	subject := auth.Subject{
		UserID:   identity.AuthUserID,
		Roles:    identity.Roles,
		AgentIDs: assignedIDs,
	}

	// Helper to check if user can access a specific agent.
	canAccess := func(agentID string) bool {
		agent, err := store.GetAgent(ctx, agentID)
		if err != nil {
			return false
		}
		req := auth.AccessRequest{
			Subject: subject,
			Action:  auth.ActionExecute,
			Resource: auth.Resource{
				Type:  auth.ResourceAgent,
				ID:    agent.ID,
				Attrs: map[string]any{"scope": agent.Scope},
			},
		}
		return engine.Can(ctx, req)
	}

	// Group chat: look up per-group agent assignment.
	if chat.IsGroup && chat.ChatID != "" {
		agentID, err := store.GetChatAgent(ctx, chat.Platform, chat.ChatID)
		if err == nil && agentID != "" {
			if !canAccess(agentID) {
				log.Warn("agent access denied for group chat", "agent_id", agentID)
				return "", ErrAgentAccessDenied
			}
			return agentID, nil
		}
	}

	// DM: use user's default agent.
	if !chat.IsGroup && user.DefaultAgentID != "" {
		if !canAccess(user.DefaultAgentID) {
			log.Warn("agent access denied for DM default", "agent_id", user.DefaultAgentID)
			return "", ErrAgentAccessDenied
		}
		return user.DefaultAgentID, nil
	}

	// Fallback: first enabled agent the user can access.
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve agent: list enabled agents: %w", err)
	}
	for _, a := range agents {
		req := auth.AccessRequest{
			Subject: subject,
			Action:  auth.ActionExecute,
			Resource: auth.Resource{
				Type:  auth.ResourceAgent,
				ID:    a.ID,
				Attrs: map[string]any{"scope": a.Scope},
			},
		}
		if engine.Can(ctx, req) {
			return a.ID, nil
		}
	}

	return "", fmt.Errorf("resolve agent: no accessible enabled agents found")
}
