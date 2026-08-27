package agent

import "github.com/CherryHQ/stella/pkg/ai"

// LoopEvent is the runtime event contract emitted by the agent loop.
type LoopEvent interface {
	Kind() string
}

// AgentStarted is emitted when a loop begins.
type AgentStarted struct{}

func (AgentStarted) Kind() string { return "agentStarted" }

// AssistantStarted is emitted when assistant streaming begins.
type AssistantStarted struct {
	Message ai.AssistantMessage
}

func (AssistantStarted) Kind() string { return "assistantStarted" }

// AssistantDelta forwards an incremental provider event with the current partial message.
type AssistantDelta struct {
	Event   ai.AssistantEvent
	Message ai.AssistantMessage
}

func (AssistantDelta) Kind() string { return "assistantDelta" }

// AssistantFinished is emitted when the final assistant message is assembled.
type AssistantFinished struct {
	Message ai.AssistantMessage
}

func (AssistantFinished) Kind() string { return "assistantFinished" }

// TurnStarted is emitted at the start of each loop turn.
type TurnStarted struct {
	Turn int
}

func (TurnStarted) Kind() string { return "turnStarted" }

// TurnFinished is emitted at the end of each loop turn.
type TurnFinished struct {
	Turn int
}

func (TurnFinished) Kind() string { return "turnFinished" }

// ToolStarted is emitted for each tool invocation.
type ToolStarted struct {
	ToolCall ai.ToolCall
}

func (ToolStarted) Kind() string { return "toolStarted" }

// ToolFinished is emitted when tool returns.
type ToolFinished struct {
	Result ai.ToolResultMessage
}

func (ToolFinished) Kind() string { return "toolFinished" }

// ChildToolStarted is emitted when an outer Code call reaches a child handler.
// Runtime adapters may render it, but must never persist it as provider history.
type ChildToolStarted struct {
	ParentToolCallID string
	ToolCall         ai.ToolCall
}

func (ChildToolStarted) Kind() string { return "childToolStarted" }

// ChildToolFinished is emitted for every settled Code child attempt, including
// policy blocks and missing tools. It is runtime-only and has no storage form.
type ChildToolFinished struct {
	ParentToolCallID string
	Result           ai.ToolResultMessage
}

func (ChildToolFinished) Kind() string { return "childToolFinished" }

// AgentFinished is emitted when loop completes.
type AgentFinished struct{}

func (AgentFinished) Kind() string { return "agentFinished" }

// AgentErrored is emitted for terminal loop errors.
type AgentErrored struct {
	Err error
}

func (AgentErrored) Kind() string { return "agentErrored" }
