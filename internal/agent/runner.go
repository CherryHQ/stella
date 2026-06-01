package agent

import (
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
)

// Type aliases — all agent stream types are now defined in internal/agent/runtime.
// Callers that import "internal/agent" continue to work unchanged.

type (
	// ToolUseEvent describes a tool invocation in progress or completed.
	ToolUseEvent = agentruntime.ToolUseEvent
	// StepEvent marks the boundary of an agentic step.
	StepEvent = agentruntime.StepEvent
	// ImageEvent carries a base64-encoded image to be sent to the channel.
	ImageEvent = agentruntime.ImageEvent
	// FileEvent carries a local file path to be sent to the channel.
	FileEvent = agentruntime.FileEvent
	// Event is the consumer-facing stream event.
	Event = agentruntime.Event
	// Runner executes prompts against an AI backend.
	Runner = agentruntime.Runner
	// RunnerParams holds parameters for creating a new Runner instance.
	RunnerParams = agentruntime.RunnerParams
	// NewRunnerFunc creates a new Runner with the given params.
	NewRunnerFunc = agentruntime.NewRunnerFunc
)

// MessageContent is the type for user messages passed through the runner pipeline.
// It is either string (text-only) or []ai.ContentBlock (multimodal).
type MessageContent = agentruntime.MessageContent

// MessageText extracts and joins all text from a MessageContent.
var MessageText = agentruntime.MessageText
