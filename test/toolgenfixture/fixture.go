// Package toolgenfixture pins toolgen's declaration-only path against a real
// package: agent-tools/session.yaml is generated into tool_gen.go here, and
// this file supplies both the hand-written type the generated names must not
// collide with and a Handler implementation the generated Dispatch must accept.
//
// It exists outside internal/ on purpose. toolgen prunes stale generated files
// under its output root, and a fixture living there would be deleted on the
// next run.
package toolgenfixture

import "context"

// SendInput is hand-written, and shares its name with what a naive generator
// would emit for session/send. internal/agent/session/access has exactly this
// collision; the generated type is SessionSendInput.
type SendInput struct {
	Authority string
	SessionID string
}

// Handlers implements the generated Handler interface.
type Handlers struct{}

func (Handlers) List(context.Context, SessionListInput) (any, error) { return nil, nil }

func (Handlers) Send(_ context.Context, in SessionSendInput) (any, error) { return in.SessionId, nil }

var _ Handler = Handlers{}
