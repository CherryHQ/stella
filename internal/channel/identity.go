package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

var ErrAgentAccessDenied = errors.New("you don't have access to this agent, contact an admin")

type ChatContext struct {
	Platform  string
	ChannelID string
	ChatID    string
	IsGroup   bool
}

type ResolvedIdentity struct {
	User auth.AuthUser
}

type identityMatch struct {
	Identity auth.Identity
	Matched  string
}

func ResolveUser(ctx context.Context, authStore auth.AuthStore, platform, externalID string) (ResolvedIdentity, error) {
	resolved, _, err := ResolveUserCandidates(ctx, authStore, platform, []string{externalID})
	return resolved, err
}

func ResolveUserCandidates(ctx context.Context, authStore auth.AuthStore, platform string, externalIDs []string) (ResolvedIdentity, identityMatch, error) {
	candidates := orderedIDs(externalIDs...)
	log := slog.With("component", "identity", "platform", platform, "external_ids", candidates)
	if len(candidates) == 0 {
		log.Debug("no sender ids available for identity resolution")
		return ResolvedIdentity{}, identityMatch{}, nil
	}

	var lastNotFound error
	for _, externalID := range candidates {
		identity, err := authStore.GetIdentityByPlatform(ctx, platform, externalID)
		if err != nil {
			if isNotFound(err) {
				lastNotFound = err
				continue
			}
			return ResolvedIdentity{}, identityMatch{}, fmt.Errorf("lookup identity: %w", err)
		}

		user, err := authStore.GetUser(ctx, identity.UserID)
		if err != nil {
			if isNotFound(err) {
				log.Error("auth user not found for linked identity", "user_id", identity.UserID, "external_id", externalID, "error", err)
				return ResolvedIdentity{}, identityMatch{}, nil
			}
			return ResolvedIdentity{}, identityMatch{}, fmt.Errorf("lookup auth user: %w", err)
		}
		if !user.IsActive {
			return ResolvedIdentity{}, identityMatch{}, fmt.Errorf("account is deactivated")
		}

		return ResolvedIdentity{User: user}, identityMatch{Identity: identity, Matched: externalID}, nil
	}

	if lastNotFound != nil {
		log.Debug("no linked identity found, user must link via link code")
	}
	return ResolvedIdentity{}, identityMatch{}, nil
}

func orderedIDs(ids ...string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func maybeCanonicalizeIdentity(ctx context.Context, authStore auth.AuthStore, platform, preferredID string, match identityMatch) error {
	if preferredID == "" || match.Identity.ID == "" || match.Identity.ExternalID == preferredID {
		return nil
	}

	log := slog.With(
		"component", "identity",
		"platform", platform,
		"identity_id", match.Identity.ID,
		"from_external_id", match.Identity.ExternalID,
		"to_external_id", preferredID,
		"user_id", match.Identity.UserID,
	)

	preferred, err := authStore.GetIdentityByPlatform(ctx, platform, preferredID)
	if err == nil {
		if preferred.UserID != match.Identity.UserID {
			log.Warn("cannot canonicalize identity: preferred external_id is linked to another user", "conflict_user_id", preferred.UserID)
			return nil
		}
		if preferred.ID != match.Identity.ID {
			if err := authStore.DeleteIdentity(ctx, match.Identity.ID); err != nil {
				return fmt.Errorf("delete duplicate identity after canonicalization: %w", err)
			}
			log.Info("deleted duplicate legacy identity after canonicalization", "kept_identity_id", preferred.ID)
		}
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("lookup canonical identity: %w", err)
	}

	if err := authStore.UpdateIdentityExternalID(ctx, match.Identity.ID, preferredID); err != nil {
		return fmt.Errorf("canonicalize identity: %w", err)
	}
	log.Info("canonicalized identity to preferred external_id")
	return nil
}

func ResolveAgent(ctx context.Context, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, identity ResolvedIdentity, chat ChatContext) (string, error) {
	log := slog.With("component", "identity", "user_id", identity.User.ID)

	if identity.User.ID == 0 {
		log.Warn("unlinked user denied access")
		return "", ErrAgentAccessDenied
	}

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

	channelID := chat.ChannelID
	if channelID == "" {
		channelID = chat.Platform
	}
	if channelID != "" {
		ch, err := store.GetChannel(ctx, channelID)
		if err == nil && ch.AgentID != "" {
			agent, agentErr := store.GetAgent(ctx, ch.AgentID)
			if agentErr != nil {
				return "", fmt.Errorf("resolve agent: dedicated channel agent %q not found", ch.AgentID)
			}
			if !agent.Enabled {
				return "", fmt.Errorf("resolve agent: dedicated channel agent %q is not enabled", ch.AgentID)
			}
			return ch.AgentID, nil
		}
	}

	if chat.IsGroup && chat.ChatID != "" {
		agentID, err := store.GetChatAgent(ctx, channelID, chat.Platform, chat.ChatID)
		if err == nil && agentID != "" {
			if canAccess(agentID) {
				return agentID, nil
			}
			log.Warn("agent access denied for group chat, falling back", "agent_id", agentID)
		}
	}

	if !chat.IsGroup && identity.User.DefaultAgentID != "" {
		if canAccess(identity.User.DefaultAgentID) {
			return identity.User.DefaultAgentID, nil
		}
		log.Warn("agent access denied for DM default, falling back", "agent_id", identity.User.DefaultAgentID)
	}

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

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
