package engine

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/ai"
)

// Continue validates that the transcript tail is a user or tool-result message
// and resumes the agent loop from the existing history.
func (e *Engine) Continue(ctx context.Context, cfg LoopConfig, history []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	if len(history) == 0 {
		return nil, errors.New("cannot continue empty history")
	}

	switch history[len(history)-1].(type) {
	case ai.UserMessage, ai.ToolResultMessage:
		return e.Run(ctx, cfg, history, emit)
	default:
		return nil, errors.New("invalid transcript tail for continue")
	}
}
