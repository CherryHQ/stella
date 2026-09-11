package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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
	var runtimeOpts []agentruntime.Option
	if len(input.ExcludedTools) > 0 {
		runtimeOpts = append(runtimeOpts, agentruntime.WithExcludedTools(input.ExcludedTools...))
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
		RuntimeOpts:      append(runtimeOpts, agentruntime.WithRunID(r.ID)),
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
	if result != "success" {
		return nil
	}
	var addr agentrun.ReplyAddress
	if err := json.Unmarshal(r.ReplyAddress, &addr); err != nil {
		return fmt.Errorf("run reply address: %w", err)
	}
	if addr.ChannelID == "" {
		// Web/API runs have no channel target — the reply is observed through
		// the durable session event stream instead of an outbox send.
		return nil
	}
	outAddr := choutbox.Address{
		V:          choutbox.AddressVersion,
		ChatKey:    addr.ChatKey,
		ThreadKey:  addr.ThreadKey,
		ReplyToKey: addr.ReplyToKey,
		Scope:      addr.Scope,
		Token:      addr.Token,
	}
	// The reply op carries the recorded turn events so the owning adapter can
	// replay them through its draft/edit surface and deliver attachments —
	// the same replay-at-send contract group replies already use. An
	// attachment-only turn (no final text) still delivers through this path;
	// a turn with nothing deliverable keeps the old send-nothing behavior.
	if events, ok := c.replyEvents(ctx, tx, r); ok && deliverable(events, reply) {
		op, err := choutbox.ReplyOp(choutbox.DeliveryKeyForRun(r.ID), addr.ChannelID, addr.AccountKey, outAddr, r.SessionID, events)
		if err != nil {
			return err
		}
		return c.outboxStore().Append(ctx, tx, []choutbox.Op{op})
	}
	if reply == "" {
		return nil
	}
	ops, err := choutbox.ReplyOps(r.ID, choutbox.DeliveryKeyForRun(r.ID), addr.ChannelID, addr.AccountKey, outAddr, reply, c.replyTextLimit(addr.ChannelID))
	if err != nil {
		return err
	}
	return c.outboxStore().Append(ctx, tx, ops)
}

// replyEvents reads the run's persisted event stream inside the finish
// transaction and converts it to the channel-facing shape. False means the
// log is empty or the payload exceeds the reply buffer — the caller falls
// back to a plain send_text delivery of the final text.
func (c *Coordinator) replyEvents(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun) ([]pkgchannel.Event, bool) {
	q := sqlc.New(tx)
	var events []pkgchannel.Event
	var seq int64
	used := 0
	for {
		rows, err := q.ReadSessionEventsForRun(ctx, sqlc.ReadSessionEventsForRunParams{
			SessionID: r.SessionID,
			RunID:     pgtype.Text{String: r.ID, Valid: true},
			Seq:       seq,
			Limit:     500,
		})
		if err != nil {
			return nil, false
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			evt, err := agentruntime.DecodeEvent(row.Event)
			if err != nil {
				continue
			}
			conv := convertEvent(evt)
			enc, err := json.Marshal(conv)
			if err != nil {
				continue
			}
			used += len(enc)
			if used > defaultGroupReplyBufferBytes {
				return nil, false
			}
			events = append(events, conv)
			seq = row.Seq
		}
		if len(rows) < 500 {
			break
		}
	}
	return events, len(events) > 0
}

// deliverable reports whether a completed turn produced user-visible output:
// final text, an image, or a file. Tool events and reasoning alone are not a
// reply worth a platform send.
func deliverable(events []pkgchannel.Event, reply string) bool {
	if reply != "" {
		return true
	}
	for _, evt := range events {
		if evt.Text != "" || evt.Image != nil || evt.File != nil {
			return true
		}
	}
	return false
}
