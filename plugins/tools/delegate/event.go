package delegate

import "time"

// SubDelegateStarted is emitted when a subagent task begins execution.
type SubDelegateStarted struct {
	TaskID string `json:"taskId"`
	Preset string `json:"preset,omitempty"`
}

func (SubDelegateStarted) Kind() string { return "subDelegateStarted" }

// SubDelegateFinished is emitted when a subagent task completes.
type SubDelegateFinished struct {
	TaskID   string        `json:"taskId"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

func (SubDelegateFinished) Kind() string { return "subDelegateFinished" }
