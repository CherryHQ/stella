package runtime

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent/session"
)

// Existing synchronous tests use the production admission protocol, including
// turn capture and cleanup, instead of maintaining a second execution path.
func (rt *Runtime) chat(ctx context.Context, out chan<- Event, info session.Info, msg MessageContent, co chatOptions) {
	defer close(out)
	stream, err := rt.ChatAdmitted(ctx, info, msg, func(options *chatOptions) { *options = co })
	if err != nil {
		out <- Event{Err: err}
		return
	}
	for event := range stream {
		out <- event
	}
}
