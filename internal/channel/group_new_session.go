package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Group `/new` rotates one agent's group session onto a fresh one.
//
// Three properties shape this file:
//
//   - The command must be intercepted before the group event log is written, in
//     both group entry points (platform ingest and the Web group send), so the
//     `/new` text never lands in any agent's assembled context.
//   - Skipping that append also skips the dedup it carries, so `/new` brings its
//     own: a receipt keyed on the inbound message id, claimed before the
//     rotation runs. Without it a platform redelivery would rotate a second
//     time and silently archive whatever the group said in between.
//   - Rotation is per agent. Each agent in a group keeps its own session
//     (BuildGroupSessionKey), so a group with several agents requires
//     `/new @agent`; a bare `/new` there returns a usage reply rather than
//     resetting everyone's context on an ambiguous command.
//
// Ordering matches the DM path in spirit but not in mechanism: group turns are
// serialized by the durable dispatcher's own per-(agent,group) queue, not by the
// coordinator's, so rotation is enqueued there and therefore runs after any
// in-flight group turn for that agent instead of racing it.

// newSessionCommand is the command string recorded on a `/new` receipt.
const newSessionCommand = "/new"

// errUnidentifiedCommand reports a destructive command on a delivery that
// carries no stable message id. Without an identity a redelivery cannot be
// told apart from a new command, so the command fails closed instead of
// running unguarded.
var errUnidentifiedCommand = errors.New("command message has no stable identity")

// commandReceipt is the durable "this inbound message's command already ran"
// marker for one group command. A receipt with no query set (a coordinator
// built without a database) runs unguarded — there is no store to guard with —
// but a delivery Stella cannot name (a platform that sends no message id, a
// Web send with no client_message_id) fails the claim with
// errUnidentifiedCommand instead: it could not be recognised on redelivery
// either, and collapsing every such message onto one shared row would be
// worse.
type commandReceipt struct {
	q         *sqlc.Queries
	groupID   string
	platform  string
	messageID string
	command   string
}

func newCommandReceipt(q *sqlc.Queries, groupID, platform, messageID, command string) commandReceipt {
	return commandReceipt{q: q, groupID: groupID, platform: platform, messageID: messageID, command: command}
}

func (r commandReceipt) inert() bool {
	return r.q == nil || r.groupID == "" || r.messageID == ""
}

// claim reserves the right to run the command once. false means the claim was
// already taken, i.e. this is a redelivery of a message whose command has run.
//
// Consumed claims are never expired: the Web API promises client_message_id
// idempotency with no time window, and ctx_group_message's dedup for ordinary
// messages is permanent too, so a TTL here would quietly reopen the destructive
// replay this receipt exists to prevent. One row per executed command keeps the
// table small on its own; rows leave with their group (ON DELETE CASCADE).
func (r commandReceipt) claim(ctx context.Context) (bool, error) {
	if r.q == nil {
		return true, nil
	}
	if r.groupID == "" || r.messageID == "" {
		return false, errUnidentifiedCommand
	}
	rows, err := r.q.CreateGroupCommandReceipt(ctx, sqlc.CreateGroupCommandReceiptParams{
		GroupID:   r.groupID,
		Platform:  r.platform,
		MessageID: r.messageID,
		Command:   r.command,
	})
	if err != nil {
		return false, fmt.Errorf("claim group command receipt: %w", err)
	}
	return rows > 0, nil
}

// release drops a claim whose command never ran, so a redelivery may retry it.
// A release failure is only logged: the claim it leaves behind costs the group
// one retry of a command the user can simply repeat.
func (r commandReceipt) release(ctx context.Context) {
	if r.inert() {
		return
	}
	// The command often failed BECAUSE the request context died (a timeout while
	// queued behind a long turn), and a release on that same context would fail
	// with it — leaving a claim for a command that never ran. Detach, but keep a
	// deadline: release is best-effort by contract.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.q.DeleteGroupCommandReceipt(ctx, sqlc.DeleteGroupCommandReceiptParams{
		GroupID:   r.groupID,
		Platform:  r.platform,
		MessageID: r.messageID,
	}); err != nil {
		slog.WarnContext(ctx, "failed to release group command receipt", "error", err,
			"group_id", r.groupID, "platform", r.platform)
	}
}

// RotateGroupSession starts a fresh session for one agent in a Web group and
// returns the user-facing reply. It runs in the same per-(agent,group) queue as
// that agent's group turns, so a rotation waits for an in-flight turn instead of
// stranding its reply in a session the group has already left.
//
// clientMessageID is the browser's idempotency token for the send, the same one
// PrepareSend hands the event log; it makes a retried `/new` a no-op.
func (d *GroupDispatcher) RotateGroupSession(ctx context.Context, groupID, agentID, clientMessageID string) (string, error) {
	rc, err := d.resolveWebGroupChat(ctx, groupID, agentID)
	if err != nil {
		return "", err
	}
	receipt := newCommandReceipt(d.q, groupID, webGroupPlatform, clientMessageID, newSessionCommand)
	return d.rotateGroupChat(ctx, rc, receipt), nil
}

// resolveWebGroupChat builds the group chat binding for an agent in a Web group.
// A persisted membership is not a standing execute grant: the authority is
// minted fresh for this exact group/member and re-authorized here. Both the Web
// group turn and the Web group `/new` enter through it, so service selection and
// the group authority live in exactly one place.
func (d *GroupDispatcher) resolveWebGroupChat(ctx context.Context, groupID, agentID string) (*ResolvedChat, error) {
	if d == nil || d.coord == nil || d.coord.agentAccess == nil {
		return nil, ErrAgentAccessDenied
	}
	authority, err := agentaccess.GroupAgentAuthority(groupID, agentID)
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	if _, err := d.coord.agentAccess.Use(ctx, authority, agentID); err != nil {
		return nil, ErrAgentAccessDenied
	}
	svc := d.coord.serviceManager.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}
	return &ResolvedChat{
		Service:    svc,
		AgentID:    agentID,
		SessionKey: agent.BuildGroupSessionKey(agentID, groupID),
		Channel:    session.Channel("group:" + groupID),
		GroupID:    groupID,
		Authority:  authority,
	}, nil
}

// commandClaim is the once-per-message guard a destructive command runs under:
// claim reserves the right to execute exactly once, release hands it back when
// the command provably never ran. The group and DM receipts implement it over
// their respective tables.
type commandClaim interface {
	claim(ctx context.Context) (bool, error)
	release(ctx context.Context)
}

// rotateGroupChat routes both group entry points through the shared rotation
// path with the group's receipt and the dispatcher's per-(agent,group) queue.
func (d *GroupDispatcher) rotateGroupChat(ctx context.Context, rc *ResolvedChat, receipt commandReceipt) string {
	var q *sessionQueue
	if d != nil {
		q = d.queue
	}
	return rotateChatSession(ctx, rc, receipt, q)
}

// rotateChatSession claims the command's receipt, then resolves the session it
// names and rotates it from inside the chat's turn queue. Every `/new` entry
// point — platform group ingest, the Web group send, and the DM coordinator —
// funnels through here, so the once-per-message guard and the ordering
// guarantee are stated in exactly one place.
//
// The two guards answer different races. The receipt stops the same inbound
// message from rotating twice, which redelivery would otherwise turn into a
// silent wipe of everything the chat said since. Resolving the current session
// outside the queue makes a genuinely different, concurrent `/new` name a
// session that is already archived, so it reports the reset as done instead of
// resetting a second time.
func rotateChatSession(ctx context.Context, rc *ResolvedChat, receipt commandClaim, queue *sessionQueue) string {
	claimed, err := receipt.claim(ctx)
	if errors.Is(err, errUnidentifiedCommand) {
		return pkgchannel.NewSessionUnverifiableMessage
	}
	if err != nil {
		// Fail closed: without the guard this delivery could be a redelivery, and
		// running it would be destructive. The user can just ask again.
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	if !claimed {
		return pkgchannel.SessionAlreadyResetMessage
	}
	current, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		receipt.release(ctx)
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	run := func(fn func(context.Context) error) (bool, error) { return true, fn(ctx) }
	if queue != nil {
		run = func(fn func(context.Context) error) (bool, error) {
			return queue.EnqueueControl(ctx, rc.queueKey(), fn)
		}
	}
	// Not NewSessionReply: it folds every outcome into a reply string, and the
	// receipt needs to know which one happened. RotateInfo is a single
	// transaction, so an error other than a stale CAS or a lost commit
	// acknowledgement means the rotation rolled back and never ran — release so
	// a redelivery may retry. A stale CAS means another `/new` already did the
	// reset this message asked for, so the claim stands and a redelivery answers
	// "already reset".
	var reply string
	started, err := run(func(qctx context.Context) error {
		switch _, err := rc.RotateSession(qctx, current.ID); {
		case err == nil:
			reply = pkgchannel.NewSessionStartedMessage
			return nil
		case errors.Is(err, session.ErrStaleRotation):
			reply = pkgchannel.SessionAlreadyResetMessage
			return nil
		default:
			return err
		}
	})
	if err != nil {
		// A lost commit acknowledgement is not an outcome: the rotation may have
		// landed. Ask the binding, which is the thing a rotation moves, before
		// telling the user anything.
		if started && errors.Is(err, session.ErrRotationOutcomeUnknown) {
			switch session.VerifyRotation(ctx, current.ID, rc.ResolveSession) {
			case session.RotationCommitted:
				return pkgchannel.NewSessionStartedMessage
			case session.RotationNotExecuted:
				// Proof of rollback, so the claim has nothing to protect.
				receipt.release(ctx)
				return pkgchannel.NewSessionNotExecutedMessage
			default:
				return pkgchannel.NewSessionOutcomeUnknownMessage
			}
		}
		// Release only when the reset provably did not happen: the queue never
		// started it, or the rotation ran and definitely rolled back. A
		// context-flavoured error after the rotation started cannot prove that —
		// the transaction may have committed in the same instant the caller gave
		// up, and the caller's context is too dead to ask the binding — so the
		// claim stands. An ambiguous claim kept costs one manual retry;
		// released wrongly it replays a destructive reset.
		ambiguous := started && (errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded))
		if !ambiguous {
			receipt.release(ctx)
		}
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	return reply
}

// handleGroupNewSessionCommand answers a platform group's `/new`. It runs before
// the event-log append, so the command itself never enters group context.
//
// Any group member may reset: the group's context is shared, so clearing it is
// treated like anything else a member can say to the agent, not as an
// administrative act. Stella has no trustworthy notion of a platform group
// admin — membership records agents and their reply channels, never human
// roles, and each platform exposes roles through a different API that several
// adapters cannot query at all, so gating on "admin" would fail closed into
// "nobody may reset" exactly where it is least expected. The reset is also
// recoverable: the predecessor is archived, not deleted, and stays searchable.
// If a deployment ever needs owner-only resets, that arrives as a channel
// capability that can answer roles for every adapter, not as an agent-ownership
// check — agent ownership and platform group authority are different things.
// Documented for users in web/content/docs/guides/memory.md.
func (c *Coordinator) handleGroupNewSessionCommand(ctx context.Context, msg pkgchannel.IncomingMessage, args string) string {
	if c.groupResolver == nil || c.memberLister == nil {
		return pkgchannel.GroupNewSessionUnavailableMessage
	}
	groupID, err := c.groupResolver.ResolveGroupID(ctx, msg.Platform, msg.ChatID, msg.ThreadID)
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	members, err := c.memberLister.ListGroupMembers(ctx, groupID)
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}

	// A platform mention carries a bot's platform id; resolve it to an agent the
	// same way a normal group turn does before matching it against the roster.
	c.resolveMentionAgentsWithMembers(ctx, groupID, msg.Platform, msg.Mentions, members)
	agentIDs := memberAgentIDs(members)
	target, usage := groupNewTarget(firstMentionedAgent(msg.Mentions), args, agentIDs)
	if target == "" {
		return usage
	}

	rc, err := c.resolveGroupChat(ctx, msg, groupID, target, findMemberReplyChannel(members, target))
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	// The receipt is claimed only once the command has a target: an ambiguous or
	// unusable `/new` changed nothing, so a redelivery may answer it again.
	receipt := newCommandReceipt(c.queries(), groupID, msg.Platform, msg.MessageID, newSessionCommand)
	return c.groupDispatcher.rotateGroupChat(ctx, rc, receipt)
}

// queries returns the coordinator's query set, or nil when it was built without
// a pool (unit tests, degraded deployments).
func (c *Coordinator) queries() *sqlc.Queries {
	if c == nil || c.db == nil {
		return nil
	}
	return sqlc.New(c.db)
}

// groupNewTarget picks the agent whose group session a `/new` rotates. A group
// with exactly one agent needs no target; anything ambiguous returns a usage
// reply instead, because resetting every agent's context on an unclear command
// is destructive by default.
func groupNewTarget(mentioned, args string, agentIDs []string) (target string, usage string) {
	if len(agentIDs) == 0 {
		return "", pkgchannel.GroupNewSessionNoAgentsMessage
	}
	named := mentioned
	if named == "" {
		named = namedAgentInArgs(args)
	}
	if named != "" {
		if !slices.Contains(agentIDs, named) {
			return "", pkgchannel.GroupNewSessionUsageMessage(agentIDs)
		}
		return named, ""
	}
	if len(agentIDs) == 1 {
		return agentIDs[0], ""
	}
	return "", pkgchannel.GroupNewSessionUsageMessage(agentIDs)
}

// namedAgentInArgs matches a literal `@agent` argument against the roster. It
// covers platforms whose mention never resolves to a registered bot identity and
// the Web group, where the agent id is the mention target directly.
func namedAgentInArgs(args string) string {
	for field := range strings.FieldsSeq(args) {
		name, ok := strings.CutPrefix(field, "@")
		if !ok {
			continue
		}
		// A named-but-unknown agent must not silently fall through to the
		// single-member shortcut; return it so the caller replies with the roster.
		return name
	}
	return ""
}

func memberAgentIDs(members []GroupMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.AgentID)
	}
	return out
}
