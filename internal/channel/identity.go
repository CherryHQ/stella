package channel

import (
	"context"
	"database/sql"
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

// ResolvedIdentity holds the resolved user.
type ResolvedIdentity struct {
	User auth.AuthUser
}

// ResolveUser resolves a channel user via auth_identities.
// If no linked identity exists, returns a zero-value user (ID=0) with no roles.
// ResolveAgent will deny access for unlinked users.
func ResolveUser(ctx context.Context, authStore auth.AuthStore, platform, externalID string) (ResolvedIdentity, error) {
	log := slog.With("component", "identity", "platform", platform, "external_id", externalID)

	identity, err := authStore.GetIdentityByPlatform(ctx, platform, externalID)
	if err != nil {
		if isNotFound(err) {
			log.Debug("no linked identity found, user must link via link code")
			return ResolvedIdentity{}, nil
		}
		return ResolvedIdentity{}, fmt.Errorf("lookup identity: %w", err)
	}

	user, err := authStore.GetUser(ctx, identity.UserID)
	if err != nil {
		if isNotFound(err) {
			log.Error("auth user not found for linked identity", "user_id", identity.UserID, "error", err)
			return ResolvedIdentity{}, nil
		}
		return ResolvedIdentity{}, fmt.Errorf("lookup auth user: %w", err)
	}
	if !user.IsActive {
		return ResolvedIdentity{}, fmt.Errorf("account is deactivated")
	}

	return ResolvedIdentity{User: user}, nil
}

// ResolveAgent determines which agent to route to, checking access via the
// policy engine. Returns ErrAgentAccessDenied if the user cannot access the
// resolved agent. Unlinked users (ID=0) are always denied.
func ResolveAgent(ctx context.Context, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, identity ResolvedIdentity, chat ChatContext) (string, error) {
	log := slog.With("component", "identity", "user_id", identity.User.ID)

	if identity.User.ID == 0 {
		log.Warn("unlinked user denied access")
		return "", ErrAgentAccessDenied
	}

	// Build subject for policy checks.
	assignedIDs, _ := authStore.ListUserAgentIDs(ctx, identity.User.ID)
	subject := auth.Subject{
		UserID:   identity.User.ID,
		Roles:    []string{identity.User.Role},
		AgentIDs: assignedIDs,
	}

	canAccess := func(agentID string) bool {
		agent, err := store.GetAgent(ctx, agentID)
		if err != nil {
			return false
		}
		return engine.Can(ctx, auth.AccessRequest{
			Subject: subject,
			Action:  auth.ActionExecute,
			Resource: auth.Resource{
				Type:  auth.ResourceAgent,
				ID:    agent.ID,
				Attrs: map[string]any{"scope": agent.Scope},
			},
		})
	}

	// Group chat: look up per-group agent assignment, fall through if denied.
	if chat.IsGroup && chat.ChatID != "" {
		agentID, err := store.GetChatAgent(ctx, chat.Platform, chat.ChatID)
		if err == nil && agentID != "" {
			if canAccess(agentID) {
				return agentID, nil
			}
			log.Warn("agent access denied for group chat, falling back", "agent_id", agentID)
		}
	}

	// DM: use user's default agent if accessible, otherwise fall through.
	if !chat.IsGroup && identity.User.DefaultAgentID != "" {
		if canAccess(identity.User.DefaultAgentID) {
			return identity.User.DefaultAgentID, nil
		}
		log.Warn("agent access denied for DM default, falling back", "agent_id", identity.User.DefaultAgentID)
	}

	// Fallback: first enabled agent the user can access.
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve agent: list enabled agents: %w", err)
	}
	for _, a := range agents {
		if canAccess(a.ID) {
			return a.ID, nil
		}
	}

	return "", fmt.Errorf("resolve agent: no accessible enabled agents found")
}

// isNotFound returns true if the error indicates a record was not found.
func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
