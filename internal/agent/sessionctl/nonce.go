// Package sessionctl gives an agent the `session_control` tool: the way a user
// can say "start a fresh session" in their own words instead of typing `/new`.
//
// Resetting a chat's context is disruptive, so the tool cannot do it in one
// step. `request_new` records a pending rotation and asks the model to put the
// question to the user; `confirm_new` performs the rotation only if the server
// can see that a real user turn answered it. The model interprets the user's
// words — that is what it is good at — while the server owns the structure:
// which chat, which speaker, which session, within which window, exactly once.
package sessionctl

import (
	"context"
	"errors"
	"time"
)

// DefaultTTL bounds how long a pending confirmation stays answerable. Long
// enough for a user to read a question and reply, short enough that a forgotten
// "should I reset?" cannot be revived by a much later "yes" about something
// else.
const DefaultTTL = 5 * time.Minute

// ErrNonceNotFound reports that no pending rotation matches the id. It covers
// "never existed", "already used", and "expired" alike: the caller is a language
// model relaying to a user, and distinguishing the three would only teach it to
// probe.
var ErrNonceNotFound = errors.New("sessionctl: no pending session rotation")

// Nonce is one pending rotation: permission the user has been asked for but has
// not yet granted. Every field is a fact the server captured itself at issue
// time, so confirming can check the answer came from the same person, in the
// same chat, about the same session, in a later turn.
type Nonce struct {
	ID string
	// SessionID is the session that was live when the question was asked. A
	// rotation from anywhere else moves the chat off it and makes this nonce
	// stale rather than dangerous.
	SessionID string
	// BindingKey identifies the durable chat (main / channel / group), so a
	// nonce issued in one chat can never be spent in another.
	BindingKey string
	// ActorID is the group speaker who asked. Empty in a DM, where the session
	// owner is the only possible actor.
	ActorID string
	// TurnMarker identifies the turn that issued the nonce. Confirming requires a
	// different marker — that is the "a real user message intervened" check.
	TurnMarker string
	ExpiresAt  time.Time
	UsedAt     time.Time
}

// Expired reports whether the nonce is past its window as of now.
func (n Nonce) Expired(now time.Time) bool { return !now.Before(n.ExpiresAt) }

// Used reports whether the nonce has already been spent.
func (n Nonce) Used() bool { return !n.UsedAt.IsZero() }

// NonceStore persists pending rotations. It is durable rather than in-process
// because the two halves of a confirmation are separate turns that may land on
// different nodes, and because a restart between them must not silently turn a
// "yes" into a no-op.
type NonceStore interface {
	// Create records a pending rotation. Implementations may opportunistically
	// drop the binding's spent and expired rows in the same call.
	Create(ctx context.Context, n Nonce) error
	// Get returns a nonce regardless of its expiry or used state, so the caller
	// can validate every condition before spending it. It returns
	// ErrNonceNotFound when the id is unknown.
	Get(ctx context.Context, id string) (Nonce, error)
	// Claim marks the nonce used, atomically and exactly once. It returns
	// ErrNonceNotFound when the nonce is already used, expired, or gone — the
	// only outcomes a caller can act on.
	Claim(ctx context.Context, id string) (Nonce, error)
}
