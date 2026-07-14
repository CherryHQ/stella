package authz

import (
	"context"
	"errors"
)

// Authorizer and Evaluation are the two interfaces in this package — they mark
// the real policy-decision security boundary and its test seam. Everything else
// in authz is a concrete immutable value: this package intentionally avoids
// one-interface-per-implementation ceremony. The concrete static built-in
// implementation lives in internal/authz/policy; this file fixes the contract
// shape that domain services and that implementation must satisfy.

// ErrAuthorizerUnavailable is the fail-closed sentinel a Begin implementation
// returns when it cannot establish an evaluation. A caller that receives it must
// deny the use case.
var ErrAuthorizerUnavailable = errors.New("authz: authorizer unavailable")

// Authorizer is the single policy-decision point. An application use case calls
// Begin once to bind itself to one immutable policy view;
// Authority is an explicit argument, never read from context. A Begin failure
// denies the use case.
type Authorizer interface {
	Begin(ctx context.Context, authority Authority) (Evaluation, error)
}

// Evaluation is an immutable policy view. Every Decide in the same use case
// observes the same rules; a new use case (each privileged tool call, durable
// worker action, or protected stream-event delivery) reacquires an Evaluation
// via Begin.
type Evaluation interface {
	// Decide answers one typed request against the bound revision.
	Decide(req Request) (Decision, error)
	// Revision returns the policy revision this evaluation is bound to.
	Revision() int64
}
