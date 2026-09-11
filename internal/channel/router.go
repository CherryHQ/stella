package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	chinbox "github.com/CherryHQ/stella/internal/channel/inbox"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/db/txlock"
)

// This file is the durable-ingress router: it drains channel_inbox for one
// channel and turns each ready event into its durable successor — a queued
// agent_run for messages, an executed binding mutation plus reply operations
// for binding commands. It deliberately never touches agent.ServiceManager,
// the local session queue, or any publisher: a replica with no in-process
// agent services can still route.
//
// Lock order follows the plan: channel advisory lock (serializes routing per
// channel across replicas) -> binding advisory lock (txlock) -> session
// advisory lock (enqueue ordering) -> agent_run. Row locks are avoided on
// purpose: the route transaction calls into session/guest resolution that
// inserts FK children of the channel/session rows on other connections, and
// a held FOR UPDATE deadlocks against their FOR KEY SHARE.

// ChannelResolver maps a channel id to its running adapter's send capability
// on this replica. Absence is not an error — a replica that owns no channels
// simply does not dispatch outbox ops.
type ChannelResolver func(channelID string) (pkgchannel.OperationSender, bool)

// WithChannelResolver binds the live-adapter lookup for outbox dispatch.
func WithChannelResolver(r ChannelResolver) CoordinatorOption {
	return func(c *Coordinator) { c.channelResolver = r }
}

// WithSessionAccess binds the Session PEP the router resolves bindings and
// rotates sessions through. Without it durable routing is unavailable.
func WithSessionAccess(svc agent.SessionAccessService) CoordinatorOption {
	return func(c *Coordinator) { c.sessionAccess = svc }
}

// WithDurableIngress switches HandleIncoming to write channel_inbox instead of
// resolving+chatting synchronously. Off by default: production entry keeps the
// old path until the run workers and outbox senders are live (Phase 3/4).
func WithDurableIngress(on bool) CoordinatorOption {
	return func(c *Coordinator) { c.durableIngress = on }
}

func (c *Coordinator) inboxStore() *chinbox.Store   { return chinbox.New(c.db) }
func (c *Coordinator) runStore() *agentrun.Store    { return agentrun.New(c.db) }
func (c *Coordinator) outboxStore() *choutbox.Store { return choutbox.New(c.db) }

// RoutePending drains pending channel_inbox events for channelID in one
// transaction: it locks the channel row, walks events in (chat_key,
// ingress_seq) order, and routes each ready event. A 'received' event blocks
// its own chat only — other chats keep routing. Returns the routed count.
func (c *Coordinator) RoutePending(ctx context.Context, channelID string) (int, error) {
	if c.db == nil || c.sessionAccess == nil {
		return 0, errors.New("channel: durable routing requires db and session access")
	}
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize routing per channel with an advisory lock, not a row lock:
	// the route transaction calls into session/guest resolution that inserts
	// FK children of the channel row on other connections, and a FOR UPDATE
	// held across those calls deadlocks against their FOR KEY SHARE.
	q := sqlc.New(tx)
	if _, err := q.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, chinbox.ErrChannelGone
		}
		return 0, err
	}
	if err := txlock.AdvisoryXactLock(ctx, tx, "channel-route:"+channelID); err != nil {
		return 0, err
	}
	events, err := c.inboxStore().ListPending(ctx, tx, channelID)
	if err != nil {
		return 0, err
	}
	blocked := map[string]bool{}
	routed := 0
	for _, ev := range events {
		if blocked[ev.ChatKey] {
			continue
		}
		if ev.State == chinbox.StateReceived {
			// Attachments not staged: nothing later in this chat may overtake it.
			blocked[ev.ChatKey] = true
			continue
		}
		if err := c.routeOne(ctx, tx, ev); err != nil {
			slog.WarnContext(ctx, "channel route failed", "inbox_id", ev.ID, "error", err)
			// A routing failure is an observable dead-end for this event, not a
			// reason to stall the channel.
			if _, terr := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateFailed, "route_error", nil); terr != nil {
				return routed, fmt.Errorf("route %s: %w (mark failed: %w)", ev.ID, err, terr)
			}
			continue
		}
		routed++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return routed, nil
}

func (c *Coordinator) routeOne(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox) error {
	env, err := chinbox.Decode(ev.Payload)
	if err != nil {
		_, terr := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateFailed, "bad_envelope", nil)
		return terr
	}
	switch ev.EventKind {
	case chinbox.KindMessage:
		if env.IsGroup {
			// Group events keep their existing event-log machinery.
			_, err := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateRejected, "group_unsupported", nil)
			return err
		}
		return c.routeMessage(ctx, tx, ev, env)
	case chinbox.KindCommand:
		return c.routeCommand(ctx, tx, ev, env)
	default:
		_, err := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateRejected, "unsupported_kind", nil)
		return err
	}
}

// routeResolve produces the pure-value ResolvedChat for one envelope: durable
// identity, agent selection, and authorization coordinates with no
// agent.Service and no session execution.
func (c *Coordinator) routeResolve(ctx context.Context, env *chinbox.Envelope, channelID string) (*ResolvedChat, error) {
	return resolveWithChannel(ctx, nil, c.store, c.auth, c.agentAccess, c.groupResolver, c.guests, c.guestPolicy,
		env.Platform, channelID, env.SenderID, env.SenderIDs, env.SenderName, env.ChatID, env.ThreadID, env.IsGroup, false)
}

// routeSession resolves (creating if needed) the session a chat is bound to.
// The advisory lock makes the binding's resolve-or-create cross-replica atomic
// where the in-process channelMu cannot reach.
func (c *Coordinator) routeSession(ctx context.Context, tx pgx.Tx, rc *ResolvedChat) (agentsession.Info, error) {
	access, err := c.sessionAccess.Begin(ctx, rc.Authority)
	if err != nil {
		return agentsession.Info{}, err
	}
	if rc.usesMainSession() {
		if err := txlock.AdvisoryXactLock(ctx, tx, "session-main:"+rc.AgentID+":"+rc.User.ID); err != nil {
			return agentsession.Info{}, err
		}
		return access.ResolveMain(ctx, rc.User.ID, rc.AgentID)
	}
	req := agentsession.ChannelRequest{
		UserID:   rc.sessionUserID(),
		AgentID:  rc.AgentID,
		GroupID:  rc.GroupID,
		GuestID:  rc.GuestID,
		Channel:  rc.Channel,
		LegacyID: rc.SessionKey,
	}
	if err := txlock.AdvisoryXactLock(ctx, tx, req.BindingLockKey()); err != nil {
		return agentsession.Info{}, err
	}
	return access.ResolveChatChannel(ctx, req)
}

func (c *Coordinator) routeMessage(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox, env *chinbox.Envelope) error {
	rc, err := c.routeResolve(ctx, env, ev.ChannelID)
	if err != nil {
		return err
	}
	allowed, err := c.channelPluginAllowed(ctx, rc)
	if err != nil {
		return err
	}
	if !allowed {
		_, err := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateRejected, "plugin_disabled", nil)
		return err
	}
	if rc.GuestID != "" && !c.guestLimiter.allow(rc.GuestID, rc.GuestMessageLimitPerMinute) {
		_, err := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateRejected, "rate_limited", nil)
		return err
	}
	info, err := c.routeSession(ctx, tx, rc)
	if err != nil {
		return err
	}
	_, _, err = c.runStore().Enqueue(ctx, tx, agentrun.EnqueueParams{
		InboxID:    ev.ID,
		SessionID:  info.ID,
		AgentID:    rc.AgentID,
		RequestKey: agentrun.RequestKeyInbox(ev.ID),
		Actor: agentrun.Actor{
			V:                agentrun.EnvelopeVersion,
			Kind:             actorKind(rc),
			UserID:           rc.User.ID,
			Role:             rc.User.Role,
			GuestID:          rc.GuestID,
			GroupID:          rc.GroupID,
			ChannelBindingID: rc.DedicatedChannelID,
			Platform:         env.Platform,
			PlatformID:       env.SenderID,
			DisplayName:      env.SenderName,
		},
		Input: agentrun.Input{
			V:       agentrun.EnvelopeVersion,
			Kind:    "message",
			Content: env.Content,
		},
		ReplyAddress: agentrun.ReplyAddress{
			V:          agentrun.EnvelopeVersion,
			ChannelID:  ev.ChannelID,
			AccountKey: ev.SourceAccountKey,
			ChatKey:    ev.ChatKey,
			ThreadKey:  env.ThreadID,
			ReplyToKey: env.MessageID,
		},
	})
	if err != nil {
		return err
	}
	if ok, err := c.inboxStore().MarkRouted(ctx, tx, ev.ID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("channel: inbox %s not routable", ev.ID)
	}
	return nil
}

func actorKind(rc *ResolvedChat) string {
	switch {
	case rc.GuestID != "":
		return "guest"
	case rc.User.ID != "":
		return "user"
	default:
		return "system"
	}
}

// replyText appends a one-shot text reply for a command event.
func (c *Coordinator) replyText(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox, env *chinbox.Envelope, text string) error {
	ops, err := choutbox.ReplyOps("", choutbox.CommandReplyKey(ev.ID), ev.ChannelID, ev.SourceAccountKey,
		choutbox.Address{
			V:          choutbox.AddressVersion,
			ChatKey:    ev.ChatKey,
			ThreadKey:  env.ThreadID,
			ReplyToKey: env.MessageID,
		}, text, c.replyTextLimit(ev.ChannelID))
	if err != nil {
		return err
	}
	if err := c.outboxStore().Append(ctx, tx, ops); err != nil {
		return err
	}
	_, err = c.inboxStore().MarkRouted(ctx, tx, ev.ID)
	return err
}

func (c *Coordinator) routeCommand(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox, env *chinbox.Envelope) error {
	rc, err := c.routeResolve(ctx, env, ev.ChannelID)
	if err != nil {
		return err
	}
	if rc.GuestID != "" {
		switch env.Command {
		case "/new", "/abort", "/help", "/compact":
		default:
			return c.replyText(ctx, tx, ev, env, "This command is not available in guest chat.")
		}
	}
	switch env.Command {
	case "/new":
		return c.routeNew(ctx, tx, ev, env, rc)
	case "/abort":
		return c.routeAbort(ctx, tx, ev, env, rc)
	default:
		// Read-only and config commands are not yet served by the durable path;
		// reject explicitly rather than silently dropping them.
		_, err := c.inboxStore().Transition(ctx, tx, ev.ID, chinbox.StateRejected, "command_unsupported", nil)
		return err
	}
}

// routeNew performs a `/new` rotation. The command receipt is claimed on the
// pool before the rotation — identical to the existing semantics — so a
// platform redelivery answers "already reset" and never rotates twice.
func (c *Coordinator) routeNew(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox, env *chinbox.Envelope, rc *ResolvedChat) error {
	receipt := chatCommandReceipt{
		q:         sqlc.New(c.db),
		channelID: ev.ChannelID,
		chatKey:   ev.ChatKey,
		messageID: env.MessageID,
		command:   newSessionCommand,
		binding:   rc.queueKey(),
	}
	claimed, err := receipt.claim(ctx)
	if errors.Is(err, errUnidentifiedCommand) {
		return c.replyText(ctx, tx, ev, env, pkgchannel.NewSessionUnverifiableMessage)
	}
	if err != nil {
		return err
	}
	if !claimed {
		return c.replyText(ctx, tx, ev, env, pkgchannel.SessionAlreadyResetMessage)
	}
	reply := pkgchannel.NewSessionStartedMessage
	fail := func(text string) error {
		receipt.release(ctx)
		return c.replyText(ctx, tx, ev, env, text)
	}
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return fail(fmt.Sprintf("Starting a new session failed: %v", err))
	}
	access, err := c.sessionAccess.Begin(ctx, rc.Authority)
	if err != nil {
		return fail(fmt.Sprintf("Starting a new session failed: %v", err))
	}
	if rc.usesMainSession() {
		if err := txlock.AdvisoryXactLock(ctx, tx, "session-main:"+rc.AgentID+":"+rc.User.ID); err != nil {
			return err
		}
		current, err := access.ResolveMain(ctx, rc.User.ID, rc.AgentID)
		if err != nil {
			return fail(fmt.Sprintf("Starting a new session failed: %v", err))
		}
		if _, err := access.RotateMain(ctx, rc.User.ID, rc.AgentID, current.ID); err != nil {
			if errors.Is(err, agentsession.ErrStaleRotation) {
				return c.replyText(ctx, tx, ev, env, pkgchannel.SessionAlreadyResetMessage)
			}
			return fail(fmt.Sprintf("Starting a new session failed: %v", err))
		}
		return c.replyText(ctx, tx, ev, env, reply)
	}
	req := agentsession.ChannelRequest{
		UserID:   rc.sessionUserID(),
		AgentID:  rc.AgentID,
		GroupID:  rc.GroupID,
		GuestID:  rc.GuestID,
		Channel:  rc.Channel,
		LegacyID: rc.SessionKey,
	}
	if err := txlock.AdvisoryXactLock(ctx, tx, req.BindingLockKey()); err != nil {
		return err
	}
	current, err := access.ResolveChatChannel(ctx, req)
	if err != nil {
		return fail(fmt.Sprintf("Starting a new session failed: %v", err))
	}
	req.ExpectedSessionID = current.ID
	if _, err := access.RotateChannel(ctx, req); err != nil {
		if errors.Is(err, agentsession.ErrStaleRotation) {
			return c.replyText(ctx, tx, ev, env, pkgchannel.SessionAlreadyResetMessage)
		}
		return fail(fmt.Sprintf("Starting a new session failed: %v", err))
	}
	return c.replyText(ctx, tx, ev, env, reply)
}

// routeAbort flags the session's live execution lease for cancellation. The
// running worker observes cancel_requested; a queued run is untouched — same
// semantics as the in-process queue abort.
func (c *Coordinator) routeAbort(ctx context.Context, tx pgx.Tx, ev sqlc.ChannelInbox, env *chinbox.Envelope, rc *ResolvedChat) error {
	info, err := c.routeSession(ctx, tx, rc)
	if err != nil {
		return err
	}
	q := sqlc.New(tx)
	row, err := q.GetSessionExecution(ctx, info.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.replyText(ctx, tx, ev, env, "No active message to abort.")
	}
	if err != nil {
		return err
	}
	n, err := q.CancelSessionExecution(ctx, sqlc.CancelSessionExecutionParams{
		SessionID: info.ID,
		Token:     row.Token,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return c.replyText(ctx, tx, ev, env, "No active message to abort.")
	}
	return c.replyText(ctx, tx, ev, env, "Aborted.")
}

// receiveDurable is the flag-gated HandleIncoming path: it lands the event in
// channel_inbox and acknowledges, returning no stream — the reply arrives
// later through channel_outbox. A platform redelivery attaches silently.
func (c *Coordinator) receiveDurable(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	env, err := chinbox.MarshalIncoming(msg, command, args)
	if err != nil {
		return "", false, nil, err
	}
	kind := chinbox.KindMessage
	if command != "" {
		kind = chinbox.KindCommand
	}
	_, _, err = c.inboxStore().Receive(ctx, chinbox.ReceiveParams{
		ChannelID:        channelID,
		SourceAccountKey: channelID,
		EventKey:         msg.MessageID,
		EventKind:        kind,
		PayloadVersion:   chinbox.EnvelopeVersion,
		Payload:          env,
		ChatKey:          choutbox.ChatKeyFor(msg),
		Ready:            true, // adapters pre-stage media via SaveAsset; unstaged platform handles arrive as envelope attachments later
	})
	if err != nil {
		return "", false, nil, err
	}
	return "", true, nil, nil
}

// RunDurableLoops drives the durable channel pipeline on this replica: a
// routing sweep over claimable channels plus the run worker (claim, execute,
// atomic finish) and its reaper. Callers start it only when durable ingress
// is enabled.
func (c *Coordinator) RunDurableLoops(ctx context.Context) {
	if c.db == nil || c.sessionAccess == nil {
		slog.WarnContext(ctx, "durable channel loops unavailable: missing db or session access")
		return
	}
	worker := agentrun.NewWorker(c.db, "worker-"+uuid.Must(uuid.NewV7()).String()[:8], c.runExecutor(), c.runFinishHook)
	go worker.Run(ctx)

	q := sqlc.New(c.db)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		channels, err := q.ListClaimableChannels(ctx)
		if err == nil {
			for _, ch := range channels {
				if _, rerr := c.RoutePending(ctx, ch.ID); rerr != nil {
					slog.WarnContext(ctx, "channel route sweep failed", "channel_id", ch.ID, "error", rerr)
				}
				if c.channelResolver != nil {
					if sender, ok := c.channelResolver(ch.ID); ok {
						if _, derr := c.outboxStore().ProcessDue(ctx, ch.ID, "", sender); derr != nil {
							slog.WarnContext(ctx, "channel outbox dispatch failed", "channel_id", ch.ID, "error", derr)
						}
					}
				}
			}
		} else if ctx.Err() == nil {
			slog.WarnContext(ctx, "channel sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
