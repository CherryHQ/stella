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
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
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
	authority, err := e.actorAuthority(ctx, actor)
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
		RuntimeOpts:      []agentruntime.Option{agentruntime.WithRunID(r.ID)},
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
//
// The persisted Role is provenance only: a user actor's current role and
// activation are reloaded here, so a queued run cannot execute under a role
// that was revoked or downgraded while it waited.
func (e runExecutor) actorAuthority(ctx context.Context, actor agentrun.Actor) (authz.Authority, error) {
	switch actor.Kind {
	case "guest":
		return authz.NewGuestAuthority(authz.GuestID(actor.GuestID), actor.ChannelBindingID)
	case "system":
		return authz.NewSystemAuthority("channel-run-worker")
	default:
		user, err := sqlc.New(e.c.db).GetAuthUser(ctx, actor.UserID)
		if err != nil {
			return authz.Authority{}, fmt.Errorf("%w: user %s unreadable", agentrun.ErrTargetGone, actor.UserID)
		}
		if !user.IsActive {
			return authz.Authority{}, fmt.Errorf("%w: user %s deactivated", agentrun.ErrTargetGone, actor.UserID)
		}
		role := user.Role
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

// replyTextLimit maps the channel's platform to its message length budget so
// reply ops are produced already split. Unknown platforms get no split — the
// adapter reports a permanent failure instead of silently truncating.
func (c *Coordinator) replyTextLimit(channelID string) int {
	ch, err := c.store.GetChannel(context.Background(), channelID)
	if err != nil {
		return 0
	}
	switch ch.Type {
	case "telegram":
		return 4000
	case "discord", "qq", "feishu", "dingtalk", "weixin":
		return 2000
	default:
		return 0
	}
}

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
			Scope:      addr.Scope,
			Token:      addr.Token,
		}, reply, c.replyTextLimit(addr.ChannelID))
	if err != nil {
		return err
	}
	return c.outboxStore().Append(ctx, tx, ops)
}
