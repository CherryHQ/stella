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
func run(ctx context.Context, cfg loopConfig, history []ai.Message, activeStart int, emit func(LoopEvent)) ([]ai.Message, error) {
	if cfg.Stream == nil {
		return nil, errors.New("agent: stream not configured")
	}
	if emit != nil {
		emit(AgentStarted{})
	}

	history, err := runLoop(ctx, cfg, history, activeStart, emit)
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

func runLoop(ctx context.Context, cfg loopConfig, history []ai.Message, activeStart int, emit func(LoopEvent)) ([]ai.Message, error) {
	if activeStart < 0 || activeStart > len(history) {
		return history, projectionError("invalid active boundary")
	}
	loopStart := time.Now()
	hydrationMemo := make(map[string]ai.ImageContent)
	for turn := 1; ; turn++ {
		if cfg.TurnNotify != nil {
			if msg := cfg.TurnNotify(turn, time.Since(loopStart)); msg != nil {
				history = append(history, ai.UserMessage{Content: *msg})
			}
		}

		if emit != nil {
			emit(TurnStarted{Turn: turn})
		}

		// PreLLMCall hooks: may modify system prompt, tool definitions, or model.
		effectiveSystem := cfg.System
		effectiveToolDefs := cfg.ToolDefinitions
		effectiveModel := cfg.Model
		streamCtx := ctx // enriched by hooks with trace spans if available
		if !cfg.Hooks.Empty() {
			preCtx := &hooks.PreLLMCallContext{
				HookMeta:        cfg.HookMeta,
				Model:           cfg.Model.Name,
				System:          cfg.System,
				ToolDefinitions: cfg.ToolDefinitions,
				MessageCount:    len(history),
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
				// Hooks can currently replace only a model name, not its full
				// capability metadata. Do not inherit image support from a different
				// configured model and risk sending pixels to an unknown target.
				if effectiveModel.Name != cfg.Model.Name {
					effectiveModel.Input = nil
				}
			}
			if hookResult.Context != nil {
				streamCtx = hookResult.Context
			}
		}

		// Build a per-turn config with hook mutations applied.
		// NOTE: shallow copy — safe because loopConfig fields are value types
		// or slices replaced wholesale (never mutated in place).
		turnCfg := cfg
		turnCfg.System = effectiveSystem
		turnCfg.Model = effectiveModel
		// Hooks control visibility, never only the provider-facing slice. The
		// exact same immutable intersection feeds the direct dispatch and the
		// code catalog/bridge, so forged names fail closed.
		effectiveTools, effectiveToolDefs := effectiveToolSnapshot(effectiveToolDefs, cfg.Tools)
		turnCfg.Tools = effectiveTools
		directTools, codeTools, providerDefs, codeToolDefs := codeModeToolSurface(effectiveTools, effectiveToolDefs, turnCfg.CodeToolSurface)
		turnCfg.ToolDefinitions = providerDefs

		// Project before normalization so synthetic inserts cannot shift the
		// active boundary. The hydration memo is local to this Run.
		projected, err := projectImages(streamCtx, turnCfg, history, activeStart, hydrationMemo)
		if err != nil {
			return history, err
		}
		// Normalize only provider-ready content.
		normalized := ai.TransformMessages(projected)
		if err := validateProviderImages(turnCfg.Model, normalized); err != nil {
			return history, err
		}

		// Scope the call so the HTTP transport can count provider attempts
		// (SDK retries are invisible from here) and span each one.
		streamCtx, modelReq := ai.WithModelRequest(streamCtx, effectiveModel.Name)

		start := time.Now()
		result, err := streamAssistant(streamCtx, normalized, turnCfg, emit)
		duration := time.Since(start)
		complete := result.Message

		// PostLLMCall hooks: telemetry / observation.
		if !cfg.Hooks.Empty() {
			usage := complete.Usage.WithCost(effectiveModel.Cost)
			postCtx := &hooks.PostLLMCallContext{
				HookMeta:         cfg.HookMeta,
				Model:            effectiveModel.Name,
				Provider:         effectiveModel.Provider,
				API:              effectiveModel.API,
				BaseURL:          effectiveModel.BaseURL,
				Usage:            usage,
				StopReason:       complete.StopReason,
				Duration:         duration,
				TimeToFirstToken: result.TimeToFirstToken,
				Attempts:         modelReq.Attempts(),
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

		toolExecCtx := tools.WithParentImageCapability(ctx, turnCfg.Model.ImageCapability())
		var canonicalizer ToolImageCanonicalizer
		if cfg.CanonicalImages != nil {
			canonicalizer = cfg.CanonicalImages.CanonicalizeToolResult
		}
		callbacks := toolCallbacks{
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
			onChildStart: func(parentID string, call ai.ToolCall) {
				if emit != nil {
					call.Arguments = redactChildArguments(call.Arguments, cfg.SecretValues)
					emit(ChildToolStarted{ParentToolCallID: parentID, ToolCall: call})
				}
			},
			onChildFinish: func(parentID string, result ai.ToolResultMessage) {
				if emit != nil {
					emit(ChildToolFinished{ParentToolCallID: parentID, Result: redactChildResult(result, cfg.SecretValues)})
				}
			},
		}
		results, err := executeCodeModeCalls(toolExecCtx, calls, directTools, codeTools, codeToolDefs, callbacks, cfg.Hooks, cfg.HookMeta, cfg.ToolLifecycle, canonicalizer)
		for _, result := range results {
			history = append(history, result)
		}
		// Keep results completed before a later call failed: callers depend on
		// that durable prefix.
		if err != nil {
			return history, err
		}
		if hasTerminalCodeResult(results) {
			// Cancellation and deadline already have a durable outer tool result.
			// Do not ask the provider for another turn after that terminal outcome.
			if emit != nil {
				emit(TurnFinished{Turn: turn})
			}
			return history, nil
		}

		if emit != nil {
			emit(TurnFinished{Turn: turn})
		}
	}
}

func hasTerminalCodeResult(results []ai.ToolResultMessage) bool {
	for _, result := range results {
		details, ok := result.Details.(codeExecutionDetails)
		if ok && details.Terminal {
			return true
		}
	}
	return false
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
	// The map merges deltas by ID; this slice preserves provider emission order.
	toolCallOrder := make([]string, 0, 4)
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
			if _, ok := toolCalls[e.ID]; !ok {
				toolCallOrder = append(toolCallOrder, e.ID)
			}
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
			e.Usage.Reported = true
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
			partial := buildPartial(msg, text, thinking, toolCalls, toolCallOrder)
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
	for _, id := range toolCallOrder {
		call := toolCalls[id]
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

	// A turn truncated at the output limit that carries nothing back is not a
	// finished turn. It looks identical to a model that chose to say nothing:
	// zero turns, no message, no error, and the caller cannot tell the two
	// apart. Seen with a reasoning model that spent its whole output budget
	// thinking; on the Terminal-Bench baseline it silently killed 13 trials
	// across 4 tasks, each of which scored 0 without touching the container.
	// Scoped deliberately: a truncated reply that did carry text or a tool call
	// is still usable and stays a clean finish.
	if msg.StopReason == ai.StopReasonLength && len(msg.Content) == 0 {
		msg.ErrorMessage = "the model reached its output token limit before it produced a reply"
		return streamResult{Message: msg, TimeToFirstToken: ttft}, errors.New("provider: " + msg.ErrorMessage)
	}

	return streamResult{Message: msg, TimeToFirstToken: ttft}, nil
}

// buildPartial constructs a snapshot of the in-progress assistant message.
func buildPartial(base ai.AssistantMessage, text, thinking string, toolCalls map[string]ai.ToolCall, toolCallOrder []string) ai.AssistantMessage {
	partial := base
	partial.Content = make([]ai.ContentBlock, 0, 4)
	if text != "" {
		partial.Content = append(partial.Content, ai.TextContent{Text: text})
	}
	if thinking != "" {
		partial.Content = append(partial.Content, ai.ThinkingContent{Thinking: thinking})
	}
	for _, id := range toolCallOrder {
		partial.Content = append(partial.Content, toolCalls[id])
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
