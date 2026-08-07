package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent/session"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// `/new` rotates a chat onto a fresh session. Only direct messages (and linked
// private channels) can be reset this way: a group's context is shared by every
// member, so no single member's chat command may clear it, and a group `/new`
// is answered with an explicit refusal instead.
//
// Skipping nothing here would be simpler, but `/new` is destructive: it must
// bring its own dedup, because a platform redelivery that rotated a second time
// would silently archive whatever the chat said in between.

// newSessionCommand is the command string recorded on a `/new` receipt.
const newSessionCommand = "/new"

// errUnidentifiedCommand reports a destructive command on a delivery that
// carries no stable message id. Without an identity a redelivery cannot be
// told apart from a new command, so the command fails closed instead of
// running unguarded.
var errUnidentifiedCommand = errors.New("command message has no stable identity")

// commandClaim is the once-per-message guard a destructive command runs under:
// claim reserves the right to execute exactly once, release hands it back when
// the command provably never ran. The DM receipt implements it over
// channel_chat_command_receipt.
type commandClaim interface {
	claim(ctx context.Context) (bool, error)
	release(ctx context.Context)
}

// rotateChatSession claims the command's receipt, authorizes the destructive
// operation, then resolves the session it names and rotates it from inside the
// chat's turn queue, so the
// once-per-message guard and the ordering guarantee are stated in one place.
//
// The two guards answer different races. The receipt stops the same inbound
// message from rotating twice, which redelivery would otherwise turn into a
// silent wipe of everything the chat said since. Resolving the current session
// outside the queue makes a genuinely different, concurrent `/new` name a
// session that is already archived, so it reports the reset as done instead of
// resetting a second time.
func rotateChatSession(ctx context.Context, rc *ResolvedChat, receipt commandClaim, queue *sessionQueue, authorize func(context.Context) error) string {
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
	if authorize == nil {
		receipt.release(ctx)
		return "Starting a new session failed: authorization is required"
	}
	if err := authorize(ctx); err != nil {
		receipt.release(ctx)
		return fmt.Sprintf("Starting a new session failed: %v", err)
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
		if err := authorize(qctx); err != nil {
			return err
		}
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
