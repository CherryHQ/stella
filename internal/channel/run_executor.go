package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// runExecutor is the production agentrun.Executor: it re-derives authority
// from the run's serialized actor facts (never the receiving replica's
// context), takes a fresh execute decision on the exact persisted session,
// and drives the local agent.Service under the adopted lease context.
type runExecutor struct{ c *Coordinator }

func (c *Coordinator) runExecutor() agentrun.Executor { return runExecutor{c: c} }

func (e runExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (string, error) {
	c := e.c
	var actor agentrun.Actor
	var input agentrun.Input
	if err := json.Unmarshal(r.Actor, &actor); err != nil {
		return "", fmt.Errorf("run actor: %w", err)
	}
	if err := json.Unmarshal(r.Input, &input); err != nil {
		return "", fmt.Errorf("run input: %w", err)
	}
	authority, err := actorAuthority(actor)
	if err != nil {
		return "", err
	}
	if c.sessionAccess == nil {
		return "", errors.New("channel: session access not bound")
	}
	access, err := c.sessionAccess.Begin(ctx, authority)
	if err != nil {
		return "", err
	}
	// Fresh execute decision on the exact durable session: a session archived,
	// deleted, or de-authorized between routing and now fails the run as
	// target_gone rather than executing on a stale target.
	info, err := access.Use(ctx, r.AgentID, r.SessionID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", agentrun.ErrTargetGone, err)
	}
	if c.serviceManager == nil {
		return "", fmt.Errorf("agent service manager not bound on this replica")
	}
	svc := c.serviceManager.GetService(r.AgentID)
	if svc == nil {
		return "", fmt.Errorf("agent service %q not found on this replica", r.AgentID)
	}
	var content agent.MessageContent
	if len(input.Content) > 0 {
		blocks, err := ai.UnmarshalContentBlocks(input.Content)
		if err != nil {
			return "", fmt.Errorf("run content: %w", err)
		}
		content = blocks
	} else {
		content = input.Text
	}
	stream := svc.Chat(ctx, agent.ChatRequest{
		SessionID:        info.ID,
		UserID:           info.UserID,
		AgentID:          r.AgentID,
		Kind:             agentsession.Kind(info.Kind),
		GroupID:          info.GroupID,
		GuestID:          info.GuestID,
		Channel:          agentsession.Channel(info.Channel),
		TelemetryChannel: actor.Platform,
		BindingID:        info.Channel,
		Message:          content,
		Authority:        authority,
	})
	var reply strings.Builder
	var firstErr error
	for evt := range stream {
		reply.WriteString(evt.Text)
		if evt.Err != nil && firstErr == nil {
			firstErr = evt.Err
		}
	}
	return reply.String(), firstErr
}

// actorAuthority rebuilds the execution capability from persisted identity
// facts — the same constructors the ingress resolver used, so a worker on a
// different replica mints the same authority the receiver would have.
func actorAuthority(actor agentrun.Actor) (authz.Authority, error) {
	switch actor.Kind {
	case "guest":
		return authz.NewGuestAuthority(authz.GuestID(actor.GuestID), actor.ChannelBindingID)
	case "system":
		return authz.NewSystemAuthority("channel-run-worker")
	default:
		role := actor.Role
		if role == "" {
			role = auth.RoleUser
		}
		subject := auth.Subject{UserID: actor.UserID, Roles: []string{role}}
		if actor.ChannelBindingID != "" {
			return subject.ChannelAuthority(actor.ChannelBindingID)
		}
		return subject.Authority()
	}
}

var _ = agentaccess.ErrForbidden // referenced by callers via mapPublicAccessError

// runFinishHook appends the final reply's outbox operation inside the
// execution-finish transaction — the reply is durable before any send attempt.
func (c *Coordinator) runFinishHook(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
	if result != "ok" || reply == "" {
		return nil
	}
	var addr agentrun.ReplyAddress
	if err := json.Unmarshal(r.ReplyAddress, &addr); err != nil {
		return fmt.Errorf("run reply address: %w", err)
	}
	ops, err := choutbox.ReplyOps(r.ID, choutbox.DeliveryKeyForRun(r.ID), addr.ChannelID, addr.AccountKey,
		choutbox.Address{
			V:          choutbox.AddressVersion,
			ChatKey:    addr.ChatKey,
			ThreadKey:  addr.ThreadKey,
			ReplyToKey: addr.ReplyToKey,
		}, reply)
	if err != nil {
		return err
	}
	return c.outboxStore().Append(ctx, tx, ops)
}
