package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/ai"
)

// One handoff belongs to one admitted turn.
var errHandoffAlreadyBound = errors.New("agent run execution handoff is already bound")

// A failed output commit cannot be reported as success.
var errOutputNotCommitted = errors.New("turn output was not committed")

// terminalWriteTimeout bounds a terminal write so a canceled caller context or
// a broken database cannot leave a Run unsettled for its whole lease.
const terminalWriteTimeout = 5 * time.Second

// ExecutionHandoff lets the source finish a Run after its last durable write.
// The source must finish or release its lease after draining the stream, including
// error paths. Delivery acknowledgements never participate in this handoff.
type ExecutionHandoff struct {
	mu       sync.Mutex
	lease    *agentrun.Lease
	outcome  TurnOutcome
	settled  bool
	admitted bool
}

// TurnOutcome is published before stream EOF. CommitErr marks an unproven
// transcript commit; Output contains only messages whose append succeeded.
type TurnOutcome struct {
	Status    string
	Reason    string
	Output    TurnOutput
	CommitErr error
}

// TurnOutput projects committed transcript text and durable media references.
// Channel routing and other rich payloads belong to the delivery record.
type TurnOutput struct {
	Text  string
	Media []string
}

// Deliverable reports whether a channel owner has something to deliver. A
// media-only reply is deliverable; an empty or reasoning-only turn is not.
func (o TurnOutput) Deliverable() bool {
	return strings.TrimSpace(o.Text) != "" || len(o.Media) > 0
}

func NewExecutionHandoff() *ExecutionHandoff { return &ExecutionHandoff{} }

// WithExecutionHandoff claims the terminal transition for the caller. The same
// value must not be handed to two turns; the second binding is refused.
func WithExecutionHandoff(h *ExecutionHandoff) Option {
	return func(o *chatOptions) { o.handoff = h }
}

// Lease returns the admitted Run, or nil when admission never bound one. A nil
// lease means Chat* already returned an error and the runtime released
// ownership itself; there is nothing for a caller to finish.
func (h *ExecutionHandoff) Lease() *agentrun.Lease {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease
}

// Outcome reports the runtime's execution result. It is only valid after the
// admitted stream has closed: the runtime publishes it before closing the events
// channel, which is the happens-before edge the caller already holds.
func (h *ExecutionHandoff) Outcome() (TurnOutcome, bool) {
	if h == nil {
		return TurnOutcome{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.settled {
		return TurnOutcome{}, false
	}
	return h.outcome, true
}

func (h *ExecutionHandoff) bind(lease *agentrun.Lease) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.admitted {
		return errHandoffAlreadyBound
	}
	h.admitted = true
	h.lease = lease
	return nil
}

func (h *ExecutionHandoff) settle(outcome TurnOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return
	}
	h.settled = true
	h.outcome = outcome
}

// turnOutput keeps adjacent text blocks exactly as committed.
func turnOutput(committed []ai.Message) TurnOutput {
	var output TurnOutput
	var text strings.Builder
	for _, msg := range committed {
		assistant, ok := msg.(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			switch value := block.(type) {
			case ai.TextContent:
				text.WriteString(value.Text)
			case ai.ImageRefContent:
				if value.MediaID != "" {
					output.Media = append(output.Media, value.MediaID)
				}
			}
		}
	}
	output.Text = text.String()
	return output
}

// terminalContext bounds a terminal write without inheriting a caller's
// cancellation: a stopped turn must still be able to record its result.
func terminalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
}
