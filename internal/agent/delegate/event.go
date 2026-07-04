package delegate

import "time"

// DelegateStarted is emitted when a delegate task begins execution.
type DelegateStarted struct {
	TaskID string `json:"taskId"`
	Preset string `json:"preset,omitempty"`
}

func (DelegateStarted) Kind() string { return "delegateStarted" }

// DelegateFinished is emitted when a delegate task completes.
type DelegateFinished struct {
	TaskID   string        `json:"taskId"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

func (DelegateFinished) Kind() string { return "delegateFinished" }
