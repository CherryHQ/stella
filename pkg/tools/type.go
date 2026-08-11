package tools

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/pkg/ai"
)

// Definition is a callable tool definition exposed to a model.
type Definition = ai.ToolDefinition

// Tool is a tool that can be executed by the Go runner.
type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// ContentTool is an optional interface for tools whose result may include
// non-text content (e.g. images). Tools that only emit text need not implement
// it; ExecuteToolContent wraps their string output automatically.
type ContentTool interface {
	ExecuteContent(ctx context.Context, args map[string]any) ([]ai.ContentBlock, error)
}

type invalidInputError struct{ err error }

func (e invalidInputError) Error() string { return e.err.Error() }
func (e invalidInputError) Unwrap() error { return e.err }

// InvalidInput marks an error as argument validation that stopped the tool
// before its operation ran. Runtimes may use the marker to avoid charging an
// execution budget for a call the model can correct and retry.
func InvalidInput(err error) error {
	if err == nil || IsInvalidInput(err) {
		return err
	}
	return invalidInputError{err: err}
}

// IsInvalidInput reports whether a tool rejected its arguments before running
// its operation.
func IsInvalidInput(err error) bool {
	var target invalidInputError
	return errors.As(err, &target)
}

// ExecuteToolContent runs a tool and returns its result as content blocks.
// Tools implementing ContentTool are called directly; others have their string
// output wrapped in a single TextContent block. Any partial text is preserved
// alongside an error so callers can surface it.
func ExecuteToolContent(ctx context.Context, t Tool, args map[string]any) ([]ai.ContentBlock, error) {
	if ct, ok := t.(ContentTool); ok {
		return ct.ExecuteContent(ctx, args)
	}
	text, err := t.Execute(ctx, args)
	return []ai.ContentBlock{ai.TextContent{Text: text}}, err
}
