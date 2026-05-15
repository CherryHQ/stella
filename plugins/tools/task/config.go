package task

import "github.com/CherryHQ/stella/internal/tasks"

// Config holds construction parameters for TaskTool.
type Config struct {
	Service *tasks.Service
}

// New creates a TaskTool from cfg.
func New(cfg Config) *TaskTool {
	return &TaskTool{svc: cfg.Service}
}
