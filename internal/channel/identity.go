package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

var ErrAgentAccessDenied = errors.New("you don't have access to this agent, contact an admin")

type ChatContext struct {
	Platform  string
	ChannelID string
	ChatID    string
	// GroupID is the canonical persisted group identity. It is resolved before
	// any agent decision; platform chat IDs are routing input, never authority.
	GroupID string
	IsGroup bool
}

type ResolvedIdentity struct {
	User auth.User
}

type identityMatch struct {
	Identity auth.ChannelIdentity
	Matched  string
}

func ResolveUser(ctx context.Context, store channelAuthStore, platform, externalID string) (ResolvedIdentity, error) {
	resolved, _, err := ResolveUserCandidates(ctx, store, platform, []string{externalID})
	return resolved, err
}

func ResolveUserCandidates(ctx context.Context, store channelAuthStore, platform string, externalIDs []string) (ResolvedIdentity, identityMatch, error) {
	candidates := orderedIDs(externalIDs...)
	log := slog.With("component", "identity", "platform", platform, "external_ids", candidates)
	if len(candidates) == 0 {
		log.Debug("no sender ids available for identity resolution")
		return ResolvedIdentity{}, identityMatch{}, nil
	}

	var lastNotFound error
	for _, externalID := range candidates {
		identity, err := store.GetChannelIdentityByPlatform(ctx, platform, externalID)
		if err != nil {
			if isNotFound(err) {
				lastNotFound = err
				resolved, match, ok, err := linkLoginIdentityAsChannelIdentity(ctx, store, platform, externalID, "")
				if err != nil {
					return ResolvedIdentity{}, identityMatch{}, err
				}
				if ok {
					return resolved, match, nil
				}
				continue
			}
			return ResolvedIdentity{}, identityMatch{}, fmt.Errorf("lookup identity: %w", err)
		}

		user, err := store.GetUser(ctx, identity.UserID)
		if err != nil {
			if isNotFound(err) {
				log.Error("auth user not found for linked identity", "user_id", identity.UserID, "external_id", externalID, "error", err)
				return ResolvedIdentity{}, identityMatch{}, nil
			}
			return ResolvedIdentity{}, identityMatch{}, fmt.Errorf("lookup auth user: %w", err)
		}

		return ResolvedIdentity{User: user}, identityMatch{Identity: identity, Matched: externalID}, nil
	}

	if lastNotFound != nil {
		log.Debug("no linked identity found, user must link via link code")
	}
	return ResolvedIdentity{}, identityMatch{}, nil
}

func linkLoginIdentityAsChannelIdentity(ctx context.Context, store channelAuthStore, platform, externalID, senderName string) (ResolvedIdentity, identityMatch, bool, error) {
	loginIdentity, err := store.GetLoginIdentityByProvider(ctx, platform, externalID)
	if err != nil {
		if isNotFound(err) {
			return ResolvedIdentity{}, identityMatch{}, false, nil
		}
		return ResolvedIdentity{}, identityMatch{}, false, fmt.Errorf("lookup login identity: %w", err)
	}

	user, err := store.GetUser(ctx, loginIdentity.UserID)
	if err != nil {
		if isNotFound(err) {
			slog.Error("auth user not found for login identity", "user_id", loginIdentity.UserID, "provider", platform, "subject", externalID, "error", err)
			return ResolvedIdentity{}, identityMatch{}, true, nil
		}
		return ResolvedIdentity{}, identityMatch{}, false, fmt.Errorf("lookup login identity user: %w", err)
	}

	name := senderName
	if name == "" {
		name = loginIdentity.Name
	}
	identity, err := store.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.Must(uuid.NewV7()).String(),
		UserID:     user.ID,
		Platform:   platform,
		ExternalID: externalID,
		Name:       name,
	})
	if err != nil {
		// Another request may have linked the same identity first. Reuse it if it
		// points at the same user; otherwise keep the explicit channel link authoritative.
		existing, lookupErr := store.GetChannelIdentityByPlatform(ctx, platform, externalID)
		if lookupErr != nil {
			return ResolvedIdentity{}, identityMatch{}, false, fmt.Errorf("create channel identity from login identity: %w", err)
		}
		if existing.UserID != user.ID {
			slog.Warn("login identity conflicts with existing channel identity", "platform", platform, "external_id", externalID, "login_user_id", user.ID, "channel_user_id", existing.UserID)
			return ResolvedIdentity{}, identityMatch{}, true, nil
		}
		identity = existing
	}

	slog.Info("linked channel identity from login identity", "platform", platform, "external_id", externalID, "user_id", user.ID)
	return ResolvedIdentity{User: user}, identityMatch{Identity: identity, Matched: externalID}, true, nil
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

func maybeCanonicalizeIdentity(ctx context.Context, store channelAuthStore, platform, preferredID string, match identityMatch) error {
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

	preferred, err := store.GetChannelIdentityByPlatform(ctx, platform, preferredID)
	if err == nil {
		if preferred.UserID != match.Identity.UserID {
			log.Warn("cannot canonicalize identity: preferred external_id is linked to another user", "conflict_user_id", preferred.UserID)
			return nil
		}
		if preferred.ID != match.Identity.ID {
			if err := store.DeleteChannelIdentity(ctx, match.Identity.ID); err != nil {
				return fmt.Errorf("delete duplicate identity after canonicalization: %w", err)
			}
			log.Info("deleted duplicate identity after canonicalization", "kept_identity_id", preferred.ID)
		}
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("lookup canonical identity: %w", err)
	}

	if err := store.UpdateChannelIdentityExternalID(ctx, match.Identity.ID, preferredID); err != nil {
		return fmt.Errorf("canonicalize identity: %w", err)
	}
	log.Info("canonicalized identity to preferred external_id")
	return nil
}

func ResolveAgent(ctx context.Context, store config.Store, accessService *agentaccess.Service, identity ResolvedIdentity, chat ChatContext) (string, error) {
	log := slog.With("component", "identity", "user_id", identity.User.ID)

	if identity.User.ID == "" {
		log.Warn("unlinked user denied access")
		return "", ErrAgentAccessDenied
	}

	role := identity.User.Role
	if role == "" {
		role = auth.RoleUser
	}
	subject := auth.Subject{UserID: identity.User.ID, Roles: []string{role}}

	channelID := chat.ChannelID
	if channelID == "" {
		channelID = chat.Platform
	}
	if channelID != "" {
		ch, err := store.GetChannel(ctx, channelID)
		if err == nil && ch.AgentID != "" {
			// A dedicated channel is a distinct use case. Its Authority carries the
			// exact persisted channel binding and cannot be minted from message data.
			authority, err := subject.ChannelAuthority(ch.ID)
			if err != nil {
				return "", ErrAgentAccessDenied
			}
			access, err := accessService.Begin(ctx, authority)
			if err != nil {
				return "", ErrAgentAccessDenied
			}
			agent, err := access.UseDedicated(ctx, ch.AgentID, ch.ID)
			if err != nil || !agent.Enabled {
				return "", ErrAgentAccessDenied
			}
			return agent.ID, nil
		}
	}

	// One non-dedicated channel resolution is one use case: every candidate
	// decision shares this revision and assignment snapshot.
	authority, err := subject.Authority()
	if err != nil {
		return "", ErrAgentAccessDenied
	}
	access, err := accessService.Begin(ctx, authority)
	if err != nil {
		return "", ErrAgentAccessDenied
	}
	canAccess := func(agentID string) bool {
		ok, err := access.CanUse(ctx, agentID)
		return err == nil && ok
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
	return errors.Is(err, auth.ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}
