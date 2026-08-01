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
		turnCfg.ToolDefinitions = effectiveToolDefs
		turnCfg.Model = effectiveModel

		// Project before normalization so synthetic inserts cannot shift the
		// active boundary. The hydration memo is local to this Run.
		projected, projectionStats, err := projectImages(streamCtx, turnCfg, history, activeStart, hydrationMemo)
		if err != nil {
			return history, err
		}
		if cfg.ProjectionObserver != nil {
			cfg.ProjectionObserver(projectionStats)
		}
		// Normalize only provider-ready content.
		normalized := ai.TransformMessages(projected)
		if !turnCfg.LegacyImages {
			if err := validateProviderImages(turnCfg.Model, normalized); err != nil {
				return history, err
			}
		} else if err := validateNoImageRefs(normalized); err != nil {
			return history, err
		}

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

		// Only an explicit "image" declaration earns the image itself. Anything
		// else — declared text-only, or never declared — goes through the text
		// rendering ladder instead. Undeclared is treated as "cannot see" on
		// purpose: providers do not report modalities, so most models arrive
		// undeclared, and handing an image to one that cannot read it produces
		// a silent placeholder the model then burns a turn working around.
		// Rendering costs image detail; a wasted turn costs more, and declaring
		// "text, image" on the provider's model restores full fidelity.
		toolExecCtx := tools.WithVision(ctx, effectiveModel.ImageCapability() == ai.ImageSupported)
		toolExecCtx = tools.WithCanonicalImages(toolExecCtx, cfg.CanonicalImages != nil)
		var canonicalizer ToolImageCanonicalizer
		if cfg.CanonicalImages != nil {
			canonicalizer = cfg.CanonicalImages.CanonicalizeToolResult
		}
		results, err := executeToolCalls(toolExecCtx, calls, cfg.Tools, toolCallbacks{
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
		}, cfg.Hooks, cfg.HookMeta, cfg.ToolLifecycle, canonicalizer)
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
