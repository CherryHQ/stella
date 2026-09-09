// Package runcontrol contains the small, dependency-free contract shared by
// AgentRun execution and asynchronous adapters. It deliberately knows nothing
// about PostgreSQL, channels, or the internal runtime package.
package runcontrol

import (
	"context"
	"errors"
)

// Outcome is the durable result of an adapter's side effect after an AgentRun
// has produced its final event. Unknown is terminal: callers must not retry an
// effect when the acknowledgement itself could have been lost.
type Outcome string

const (
	OutcomeDelivered Outcome = "delivered"
	OutcomeFailed    Outcome = "failed"
	OutcomeDiscarded Outcome = "discarded"
	OutcomeUnknown   Outcome = "unknown"
)

var ErrInvalidOutcome = errors.New("run completion has invalid outcome")

// Valid reports whether o is one of the closed adapter outcome values.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeDelivered, OutcomeFailed, OutcomeDiscarded, OutcomeUnknown:
		return true
	default:
		return false
	}
}

// Completion is the adapter-facing half of an AgentRun terminal barrier.
// Check must be called immediately before an external side effect. Ack must
// be called once the adapter knows that effect's durable outcome.
type Completion interface {
	Check(context.Context) error
	Ack(context.Context, Outcome) error
	Done() <-chan struct{}
}
