package channel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

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

	// Not found in auth_identities — auto-migrate from settings_users.
	log.Info("auto-migrating channel user to auth system")

	username := fmt.Sprintf("%s_%s", platform, externalID)
	password := randomPassword()
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Error("hash password for auto-migration", "error", err)
		return ResolvedIdentity{User: user}, nil
	}

	authUser, err := authStore.CreateUser(ctx, username, hash)
	if err != nil {
		// May already exist (race condition or prior partial migration).
		existing, getErr := authStore.GetUserByUsername(ctx, username)
		if getErr != nil {
			log.Error("create auto-migrated auth user", "error", err)
			return ResolvedIdentity{User: user}, nil
		}
		authUser = existing
	} else {
		// Assign user role.
		_ = authStore.AssignRole(ctx, authUser.ID, auth.RoleUser)
	}

	// Create the identity link.
	displayName := name
	if displayName == "" {
		displayName = externalID
	}
	_, err = authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   platform,
		ExternalID: externalID,
		Name:       displayName,
	})
	if err != nil {
		log.Error("create auto-migrated identity", "error", err)
		// Still return the user — identity creation failed but auth user exists.
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

// randomPassword generates a random 16-byte hex password for auto-migrated users.
func randomPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("channel: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
