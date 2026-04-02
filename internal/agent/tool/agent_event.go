package tool

import "time"

// SubAgentStarted is emitted when a subagent task begins execution.
type SubAgentStarted struct {
	TaskID string `json:"taskId"`
	Preset string `json:"preset,omitempty"`
}

func (SubAgentStarted) Kind() string { return "subAgentStarted" }

// SubAgentFinished is emitted when a subagent task completes.
type SubAgentFinished struct {
	TaskID   string        `json:"taskId"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

func (SubAgentFinished) Kind() string { return "subAgentFinished" }
