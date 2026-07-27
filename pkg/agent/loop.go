package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/tools"
)

// run executes the agent loop: repeatedly generating assistant responses
// and executing tool calls until the model stops calling tools,
// the turn limit is reached, or an interrupt/error occurs.
func run(ctx context.Context, cfg loopConfig, history []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	if cfg.Stream == nil {
		return nil, errors.New("agent: stream not configured")
	}
	if emit != nil {
		emit(AgentStarted{})
	}

	history, err := runLoop(ctx, cfg, history, emit)
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

func runLoop(ctx context.Context, cfg loopConfig, history []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	loopStart := time.Now()
	toolCallCounts := make(map[string]int, len(cfg.ToolCallLimits))
	runTools := toolsWithCallLimits(cfg.Tools, cfg.ToolCallLimits, toolCallCounts)
	for turn := 1; ; turn++ {
		if cfg.TurnNotify != nil {
			if msg := cfg.TurnNotify(turn, time.Since(loopStart)); msg != nil {
				history = append(history, ai.UserMessage{Content: *msg})
			}
		}

		if emit != nil {
			emit(TurnStarted{Turn: turn})
		}

		// Normalize transcript before each model call.
		normalized := ai.TransformMessages(history)

		// PreLLMCall hooks: may modify system prompt, tool definitions, or model.
		effectiveSystem := cfg.System
		effectiveToolDefs := toolDefinitionsWithinCallLimits(
			cfg.ToolDefinitions,
			cfg.ToolCallLimits,
			toolCallCounts,
		)
		effectiveModel := cfg.Model
		streamCtx := ctx // enriched by hooks with trace spans if available
		if !cfg.Hooks.Empty() {
			preCtx := &hooks.PreLLMCallContext{
				HookMeta:        cfg.HookMeta,
				Model:           cfg.Model.Name,
				System:          cfg.System,
				ToolDefinitions: effectiveToolDefs,
				MessageCount:    len(normalized),
				API:             cfg.Model.API,
				Provider:        cfg.Model.Provider,
				BaseURL:         cfg.Model.BaseURL,
				MaxTokens:       cfg.StreamOptions.MaxTokens,
				Temperature:     cfg.StreamOptions.Temperature,
			}
			hookResult, _ := cfg.Hooks.RunPreLLMCall(ctx, preCtx)
			if hookResult.System != nil {
				effectiveSystem = *hookResult.System
			}
			if hookResult.ToolDefinitions != nil {
				effectiveToolDefs = hookResult.ToolDefinitions
			}
			if hookResult.Model != nil {
				effectiveModel = cfg.Model
				effectiveModel.Name = *hookResult.Model
			}
			if hookResult.Context != nil {
				streamCtx = hookResult.Context
			}
		}
		// A hook may return the original definitions, so enforce exhausted
		// budgets again immediately before sending tools to the provider.
		effectiveToolDefs = toolDefinitionsWithinCallLimits(
			effectiveToolDefs,
			cfg.ToolCallLimits,
			toolCallCounts,
		)

		// Build a per-turn config with hook mutations applied.
		// NOTE: shallow copy — safe because loopConfig fields are value types
		// or slices replaced wholesale (never mutated in place).
		turnCfg := cfg
		turnCfg.System = effectiveSystem
		turnCfg.ToolDefinitions = effectiveToolDefs
		turnCfg.Model = effectiveModel

		start := time.Now()
		result, err := streamAssistant(streamCtx, normalized, turnCfg, emit)
		duration := time.Since(start)
		complete := result.Message

		// PostLLMCall hooks: telemetry / observation.
		if !cfg.Hooks.Empty() {
			postCtx := &hooks.PostLLMCallContext{
				HookMeta:         cfg.HookMeta,
				Model:            effectiveModel.Name,
				Provider:         effectiveModel.Provider,
				API:              effectiveModel.API,
				BaseURL:          effectiveModel.BaseURL,
				Usage:            complete.Usage,
				StopReason:       complete.StopReason,
				Duration:         duration,
				TimeToFirstToken: result.TimeToFirstToken,
				Error:            err,
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

		toolExecCtx := tools.WithVision(ctx, effectiveModel.SupportsImage())
		results, err := executeToolCalls(toolExecCtx, calls, runTools, toolCallbacks{
			onStart: func(call ai.ToolCall) {
				if emit != nil {
					emit(ToolStarted{ToolCall: call})
				}
			},
			onFinish: func(result ai.ToolResultMessage) {
				if emit != nil {
					emit(ToolFinished{Result: result})
				}
			},
		}, cfg.Hooks, cfg.HookMeta, cfg.ToolLifecycle)
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
}

// toolsWithCallLimits wraps only configured tools with counters scoped to this
// runLoop invocation. Tool execution is sequential, so no shared synchronization
// or persistent state is needed, and concurrent Runner.Run calls remain isolated.
func toolsWithCallLimits(toolSet ToolSet, limits, callCounts map[string]int) ToolSet {
	if len(limits) == 0 {
		return toolSet
	}

	runTools := make(ToolSet, len(toolSet))
	for name, fn := range toolSet {
		maxCalls := limits[name]
		if maxCalls <= 0 {
			runTools[name] = fn
			continue
		}

		toolName := name
		toolFn := fn
		runTools[toolName] = func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			if callCounts[toolName] >= maxCalls {
				return nil, fmt.Errorf(
					"tool %s can be called at most %d times for one user request; use the available results or explain that the evidence is insufficient",
					toolName,
					maxCalls,
				)
			}
			callCounts[toolName]++
			return toolFn(ctx, call)
		}
	}
	return runTools
}

// toolDefinitionsWithinCallLimits hides exhausted tools from later model calls
// in the same user request. The execution wrapper remains the hard backstop for
// providers that emit a tool call even after its definition has been removed.
func toolDefinitionsWithinCallLimits(
	definitions []ai.ToolDefinition,
	limits, callCounts map[string]int,
) []ai.ToolDefinition {
	if len(limits) == 0 {
		return definitions
	}

	filtered := make([]ai.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		maxCalls := limits[definition.Name]
		if maxCalls > 0 && callCounts[definition.Name] >= maxCalls {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

// streamAssistant opens a provider stream, emits granular assistant events,
// and assembles the final AssistantMessage.
// streamResult bundles the assistant message with timing metadata from a stream.
type streamResult struct {
	Message          ai.AssistantMessage
	TimeToFirstToken time.Duration // elapsed from stream open to first event
}

func streamAssistant(ctx context.Context, messages []ai.Message, cfg loopConfig, emit func(LoopEvent)) (streamResult, error) {
	streamStart := time.Now()
	dumpLLMContextIfEnabled(cfg, messages)
	eventStream, err := cfg.Stream(
		ctx,
		cfg.Model,
		ai.Context{System: cfg.System, Messages: messages, Tools: cfg.ToolDefinitions},
		cfg.StreamOptions,
	)
	if err != nil {
		return streamResult{}, err
	}

	msg := ai.AssistantMessage{Content: make([]ai.ContentBlock, 0, 4)}
	var text string
	var thinking string
	toolCalls := map[string]ai.ToolCall{}
	toolArgs := map[string]string{} // accumulated raw JSON per tool call ID
	started := false
	var ttft time.Duration

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
			msg.StopReason = ai.StopReasonError
			if e.Err != nil {
				msg.ErrorMessage = e.Err.Error()
			}
		}

		// Record time-to-first-token and emit AssistantStarted on first event.
		if !started {
			ttft = time.Since(streamStart)
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
		return streamResult{Message: msg, TimeToFirstToken: ttft}, waitErr
	}

	// Surface provider-level errors that were delivered as EventError events
	// rather than as a stream-level Wait() error. Without this, the error
	// is silently swallowed: StopReason=Error causes runLoop to return nil,
	// and the caller never learns what went wrong. Providers may signal
	// StopReason=Error without any error detail; that is still a failure.
	if msg.StopReason == ai.StopReasonError {
		if msg.ErrorMessage == "" {
			msg.ErrorMessage = "stream ended with error stop reason"
		}
		return streamResult{Message: msg, TimeToFirstToken: ttft}, fmt.Errorf("provider: %s", msg.ErrorMessage)
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

	return streamResult{Message: msg, TimeToFirstToken: ttft}, nil
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
