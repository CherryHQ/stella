package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/pkg/hooks"
)

// Engine coordinates model generation and tool execution.
type Engine struct {
	Providers ai.ProviderGetter
}

// Run executes the agent loop: repeatedly generating assistant responses
// and executing tool calls until the model stops calling tools,
// the turn limit is reached, or an interrupt/error occurs.
func (e *Engine) Run(ctx context.Context, cfg LoopConfig, history []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	if e == nil || e.Providers == nil {
		return nil, errors.New("engine providers not configured")
	}
	if emit != nil {
		emit(AgentStarted{})
	}

	history, err := e.runLoop(ctx, cfg, history, emit)
	if err != nil {
		if emit != nil {
			emit(AgentErrored{Err: err})
		}
		return history, err
	}

	if emit != nil {
		emit(AgentFinished{})
	}
	return history, nil
}

func (e *Engine) runLoop(ctx context.Context, cfg LoopConfig, history []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 128
	}

	for turn := 1; turn <= maxTurns; turn++ {
		if emit != nil {
			emit(TurnStarted{Turn: turn})
		}

		// Normalize transcript before each model call.
		normalized := ai.TransformMessages(history)

		// PreLLMCall hooks: may modify system prompt, tool definitions, or model.
		effectiveSystem := cfg.System
		effectiveToolDefs := cfg.ToolDefinitions
		effectiveModel := cfg.Model
		if cfg.Hooks != nil && !cfg.Hooks.Empty() {
			preCtx := &hooks.PreLLMCallContext{
				HookMeta:        cfg.HookMeta,
				Model:           cfg.Model.Name,
				System:          cfg.System,
				ToolDefinitions: cfg.ToolDefinitions,
				MessageCount:    len(normalized),
			}
			result, _ := cfg.Hooks.RunPreLLMCall(ctx, preCtx)
			if result.System != nil {
				effectiveSystem = *result.System
			}
			if result.ToolDefinitions != nil {
				effectiveToolDefs = result.ToolDefinitions
			}
			if result.Model != nil {
				effectiveModel = cfg.Model
				effectiveModel.Name = *result.Model
			}
		}

		// Build a per-turn config with hook mutations applied.
		// NOTE: shallow copy — safe because LoopConfig fields are value types
		// or slices replaced wholesale (never mutated in place).
		turnCfg := cfg
		turnCfg.System = effectiveSystem
		turnCfg.ToolDefinitions = effectiveToolDefs
		turnCfg.Model = effectiveModel

		start := time.Now()
		complete, err := streamAssistant(normalized, turnCfg, e.Providers, emit)
		duration := time.Since(start)

		// PostLLMCall hooks: telemetry / observation.
		if cfg.Hooks != nil && !cfg.Hooks.Empty() {
			postCtx := &hooks.PostLLMCallContext{
				HookMeta:   cfg.HookMeta,
				Model:      effectiveModel.Name,
				Usage:      complete.Usage,
				StopReason: complete.StopReason,
				Duration:   duration,
				Error:      err,
			}
			cfg.Hooks.RunPostLLMCall(ctx, postCtx)
		}

		if err != nil {
			return history, err
		}

		history = append(history, complete)

		// Check stop reason for terminal conditions.
		if complete.StopReason == ai.StopReasonError || complete.StopReason == ai.StopReasonAborted {
			if emit != nil {
				emit(TurnFinished{Turn: turn})
			}
			return history, nil
		}

		calls := extractToolCalls(complete)
		if len(calls) == 0 {
			if emit != nil {
				emit(TurnFinished{Turn: turn})
			}
			return history, nil
		}

		// Check interrupt before executing tools.
		if cfg.Interrupt != nil {
			select {
			case <-cfg.Interrupt:
				if emit != nil {
					emit(TurnFinished{Turn: turn})
				}
				return history, nil
			default:
			}
		}

		results, err := ExecuteToolCalls(ctx, calls, cfg.Tools, ToolCallbacks{
			OnStart: func(call ai.ToolCall) {
				if emit != nil {
					emit(ToolStarted{ToolCall: call})
				}
			},
			OnFinish: func(result ai.ToolResultMessage) {
				if emit != nil {
					emit(ToolFinished{Result: result})
				}
			},
		}, cfg.Hooks, cfg.HookMeta)
		if err != nil {
			return history, err
		}

		for _, result := range results {
			history = append(history, result)
		}

		if emit != nil {
			emit(TurnFinished{Turn: turn})
		}
	}

	return history, fmt.Errorf("agent loop exceeded maximum turns (%d)", maxTurns)
}

// streamAssistant opens a provider stream, emits granular assistant events,
// and assembles the final AssistantMessage.
func streamAssistant(messages []ai.Message, cfg LoopConfig, providers ai.ProviderGetter, emit func(LoopEvent)) (ai.AssistantMessage, error) {
	eventStream, err := ai.Stream(
		cfg.Model,
		ai.Context{System: cfg.System, Messages: messages, Tools: cfg.ToolDefinitions},
		cfg.StreamOptions,
		providers,
	)
	if err != nil {
		return ai.AssistantMessage{}, err
	}

	msg := ai.AssistantMessage{Content: make([]ai.ContentBlock, 0, 4)}
	var text string
	var thinking string
	toolCalls := map[string]ai.ToolCall{}
	toolArgs := map[string]string{} // accumulated raw JSON per tool call ID
	started := false

	for event := range eventStream.Events() {
		switch e := event.(type) {
		case ai.EventTextDelta:
			text += e.Text
		case ai.EventThinkingDelta:
			thinking += e.Thinking
		case ai.EventToolCallDelta:
			call := toolCalls[e.ID]
			call.ID = e.ID
			if e.Name != "" {
				call.Name = e.Name
			}
			if e.Arguments != "" {
				toolArgs[e.ID] += e.Arguments
			}
			toolCalls[e.ID] = call
		case ai.EventUsage:
			msg.Usage = e.Usage
		case ai.EventStop:
			msg.StopReason = e.Reason
		case ai.EventError:
			if e.Err != nil {
				msg.ErrorMessage = e.Err.Error()
				msg.StopReason = ai.StopReasonError
			}
		}

		// Emit AssistantStarted on first event.
		if !started {
			started = true
			if emit != nil {
				emit(AssistantStarted{Message: msg})
			}
		}

		// Emit every provider event as a delta.
		if emit != nil {
			// Build current partial for the delta snapshot.
			partial := buildPartial(msg, text, thinking, toolCalls)
			emit(AssistantDelta{Event: event, Message: partial})
		}
	}

	if waitErr := eventStream.Wait(); waitErr != nil {
		return msg, waitErr
	}

	// Assemble final message.
	if text != "" {
		msg.Content = append(msg.Content, ai.TextContent{Text: text})
	}
	if thinking != "" {
		msg.Content = append(msg.Content, ai.ThinkingContent{Thinking: thinking})
	}
	for id, call := range toolCalls {
		if raw, ok := toolArgs[id]; ok && raw != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(raw), &parsed) == nil {
				call.Arguments = parsed
			}
		}
		msg.Content = append(msg.Content, call)
	}

	if emit != nil {
		emit(AssistantFinished{Message: msg})
	}

	return msg, nil
}

// buildPartial constructs a snapshot of the in-progress assistant message.
func buildPartial(base ai.AssistantMessage, text, thinking string, toolCalls map[string]ai.ToolCall) ai.AssistantMessage {
	partial := base
	partial.Content = make([]ai.ContentBlock, 0, 4)
	if text != "" {
		partial.Content = append(partial.Content, ai.TextContent{Text: text})
	}
	if thinking != "" {
		partial.Content = append(partial.Content, ai.ThinkingContent{Thinking: thinking})
	}
	for _, call := range toolCalls {
		partial.Content = append(partial.Content, call)
	}
	return partial
}

func extractToolCalls(msg ai.AssistantMessage) []ai.ToolCall {
	calls := make([]ai.ToolCall, 0, 2)
	for _, block := range msg.Content {
		call, ok := block.(ai.ToolCall)
		if ok {
			calls = append(calls, call)
		}
	}
	return calls
}
