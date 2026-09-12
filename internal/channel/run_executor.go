package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

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

func (e runExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (*agentrun.Result, error) {
	c := e.c
	var actor agentrun.Actor
	var input agentrun.Input
	if err := json.Unmarshal(r.Actor, &actor); err != nil {
		return nil, fmt.Errorf("run actor: %w", err)
	}
	if err := json.Unmarshal(r.Input, &input); err != nil {
		return nil, fmt.Errorf("run input: %w", err)
	}
	authority, err := e.actorAuthority(ctx, actor)
	if err != nil {
		return nil, err
	}
	if c.sessionAccess == nil {
		return nil, errors.New("channel: session access not bound")
	}
	access, err := c.sessionAccess.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	// Fresh execute decision on the exact durable session: a session archived,
	// deleted, or de-authorized between routing and now fails the run as
	// target_gone rather than executing on a stale target.
	info, err := access.Use(ctx, r.AgentID, r.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", agentrun.ErrTargetGone, err)
	}
	if c.serviceManager == nil {
		return nil, fmt.Errorf("agent service manager not bound on this replica")
	}
	svc := c.serviceManager.GetService(r.AgentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found on this replica", r.AgentID)
	}
	var content agent.MessageContent
	if len(input.Content) > 0 {
		blocks, err := ai.UnmarshalContentBlocks(input.Content)
		if err != nil {
			return nil, fmt.Errorf("run content: %w", err)
		}
		content = blocks
	} else {
		content = input.Text
	}
	// The reply target is fixed before the turn runs: only a run with a
	// platform outbound address collects the bounded event window its reply
	// chain needs — web/API runs return nothing but final history.
	var addr agentrun.ReplyAddress
	if err := json.Unmarshal(r.ReplyAddress, &addr); err != nil {
		return nil, fmt.Errorf("run reply address: %w", err)
	}
	collect := addr.ChannelID != ""
	res := &agentrun.Result{SessionID: r.SessionID}
	var runtimeOpts []agentruntime.Option
	if len(input.ExcludedTools) > 0 {
		runtimeOpts = append(runtimeOpts, agentruntime.WithExcludedTools(input.ExcludedTools...))
	}
	// chatCtx lets the collector cancel a turn whose events overran the reply
	// budget without touching the lease lifecycle the worker owns.
	chatCtx, cancelChat := context.WithCancel(ctx)
	defer cancelChat()
	stream := svc.Chat(chatCtx, agent.ChatRequest{
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
		RuntimeOpts: append(runtimeOpts,
			agentruntime.WithRunID(r.ID),
			// The turn's terminal history returns here instead of appending at
			// stream end — the worker's finish transaction commits it
			// atomically with the run state and reply ops.
			agentruntime.WithFinalHistorySink(&res.History)),
	})
	var events []pkgchannel.Event
	var firstErr error
	used := 0
	for evt := range stream {
		if collect {
			conv := convertEvent(evt)
			enc, err := json.Marshal(conv)
			if err != nil {
				firstErr = fmt.Errorf("encode reply event: %w", err)
				break
			}
			used += len(enc)
			if used > maxCollectedReplyBytes {
				firstErr = fmt.Errorf("reply events exceed %d-byte limit", maxCollectedReplyBytes)
				break
			}
			events = append(events, conv)
		}
		if evt.Err != nil && firstErr == nil {
			firstErr = evt.Err
		}
	}
	if firstErr != nil {
		// Stop the turn itself, then join the producer: the runtime keeps
		// forwarding until its stream closes, and an early return would wedge
		// it or leave the model running.
		cancelChat()
		for range stream {
		}
		return nil, firstErr
	}
	if err := e.prepareReply(ctx, r, addr, events, res); err != nil {
		return nil, err
	}
	return res, nil
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
	return c.replyPlanFor(channelID).TextLimit
}

// replyPlanFor maps the channel's platform to its reply decomposition: how
// the send_reply op and its sibling send_text / send_attachment ops split the
// turn's deliverable output into one durable op per platform call.
func (c *Coordinator) replyPlanFor(channelID string) choutbox.ReplyPlan {
	ch, err := c.store.GetChannel(context.Background(), channelID)
	if err != nil {
		return choutbox.ReplyPlan{PrimaryText: true}
	}
	switch ch.Type {
	case "telegram":
		return choutbox.ReplyPlan{TextLimit: 4000, PrimaryText: true, Attachments: true}
	case "discord":
		return choutbox.ReplyPlan{TextLimit: 2000, PrimaryText: true, Attachments: true}
	case "weixin":
		return choutbox.ReplyPlan{TextLimit: 2000, PrimaryText: true, Attachments: true}
	case "dingtalk":
		// Session webhooks take text only — no attachment path exists.
		return choutbox.ReplyPlan{TextLimit: 2000, PrimaryText: true}
	case "qq":
		// The primary op is the terminal stream chunk; all message text rides
		// the send_text chain. Rich media needs a public URL the events do not
		// carry, so attachments stay unsupported.
		return choutbox.ReplyPlan{TextLimit: 2000}
	default:
		// testchan and card-style platforms carry the full text in one call.
		return choutbox.ReplyPlan{PrimaryText: true, Attachments: true}
	}
}

// prepareReply builds the reply's complete outbox chain before the finish
// transaction: the platform decomposition and immutable media references are
// fixed here so the finish hook only appends frozen ops. Web/API runs have no
// channel target — their reply is observed through the durable session event
// stream instead of an outbox send.
func (e runExecutor) prepareReply(ctx context.Context, r sqlc.AgentRun, addr agentrun.ReplyAddress, events []pkgchannel.Event, res *agentrun.Result) error {
	if addr.ChannelID == "" || !deliverable(events) {
		return nil
	}
	plan := e.c.replyPlanFor(addr.ChannelID)
	// File bytes are read only when the platform actually plans attachment
	// ops — a file-less platform folds them into a text marker and must not
	// lose the whole reply to an unreadable workspace path.
	if plan.Attachments {
		if err := readReplyFiles(events); err != nil {
			return err
		}
	}
	outAddr := choutbox.Address{
		V:          choutbox.AddressVersion,
		ChatKey:    addr.ChatKey,
		ThreadKey:  addr.ThreadKey,
		ReplyToKey: addr.ReplyToKey,
		Scope:      addr.Scope,
		Token:      addr.Token,
	}
	// The reply decomposes into one durable op per external call: the primary
	// segment (or draft finalize), then one op per overflow chunk and one per
	// attachment, dependency-chained so a mid-chain retry never resends a
	// landed segment. An attachment-only turn still delivers through this
	// path; a turn with nothing deliverable keeps the send-nothing behavior.
	ops, err := choutbox.ReplyChain(r.ID, choutbox.DeliveryKeyForRun(r.ID), addr.ChannelID, addr.AccountKey, outAddr, r.SessionID, events, plan)
	if err != nil {
		return err
	}
	res.Ops = ops
	return nil
}

// readReplyFiles loads every file event's bytes off its mutable workspace
// path into the event's transient Data, then drops the path — the op's
// durable copy is written to channel_outbox_attachment with the send
// intent, so nothing downstream ever re-reads a path another replica may
// not share. A file that cannot be read fails reply preparation: the run
// fails loudly instead of enqueueing an undeliverable op.
func readReplyFiles(events []pkgchannel.Event) error {
	for _, evt := range events {
		f := evt.File
		if f == nil || f.Path == "" {
			continue
		}
		if f.Name == "" {
			// Freeze the display name before the path is dropped — text-only
			// renderings and the op payload have no other name source.
			f.Name = filepath.Base(f.Path)
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return fmt.Errorf("read reply attachment %q: %w", f.Name, err)
		}
		f.MimeType = mime.TypeByExtension(filepath.Ext(f.Name))
		if f.MimeType == "" {
			f.MimeType = http.DetectContentType(data[:min(len(data), 512)])
		}
		if data == nil {
			data = []byte{} // an empty file is a real zero-byte attachment
		}
		f.Data = data
		f.Path = ""
	}
	return nil
}

// runFinishHook appends the prepared reply outbox chain inside the
// execution-finish transaction — the reply is durable before any send attempt.
func (c *Coordinator) runFinishHook(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result string, res *agentrun.Result) error {
	if result != "success" || res == nil || len(res.Ops) == 0 {
		return nil
	}
	return c.outboxStore().Append(ctx, tx, res.Ops)
}

// deliverable reports whether a completed turn produced user-visible output:
// final text, an image, or a file. Tool events and reasoning alone are not a
// reply worth a platform send.
func deliverable(events []pkgchannel.Event) bool {
	for _, evt := range events {
		if evt.Text != "" || evt.Image != nil || evt.File != nil {
			return true
		}
	}
	return false
}
