package config

import (
	"path/filepath"
	"time"
)

// RunnerConfig configures the agent runner.
type RunnerConfig struct {
	System          string           `json:"system"`
	IdleTimeout     int              `json:"idle_timeout"`
	DelegateTimeout int              `json:"delegate_timeout"` // minutes; 0 = use default (15m)
	Compaction      CompactionConfig `json:"compaction"`
}

// DelegateTimeoutDuration returns the configured delegate timeout as a
// time.Duration. Zero means "use the built-in default" (15 minutes).
func (c RunnerConfig) DelegateTimeoutDuration() time.Duration {
	if c.DelegateTimeout <= 0 {
		return 0
	}
	return time.Duration(c.DelegateTimeout) * time.Minute
}

// CompactionConfig controls automatic session compaction.
type CompactionConfig struct {
	// MaxTokens triggers compaction when the estimated token count exceeds this.
	// 0 (or omitted) uses the default of 80000. Negative values disable
	// automatic compaction. Manual /compact still works.
	MaxTokens int `json:"max_tokens"`
	// KeepTail is the number of recent message entries to preserve verbatim
	// after compaction. Default: 20.
	KeepTail int `json:"keep_tail"`
}

// SchedulerConfig configures the scheduler subsystem.
type SchedulerConfig struct {
	Enabled *bool  `json:"enabled"`
	DataDir string `json:"data_dir"`
}

// IsEnabled returns whether the scheduler is enabled (defaults to true).
func (c SchedulerConfig) IsEnabled() bool {
	return boolDefault(c.Enabled, true)
}

// HeartbeatConfig configures periodic heartbeat checks.
type HeartbeatConfig struct {
	Enabled *bool  `json:"enabled"`
	Every   string `json:"every"`
	File    string `json:"file"`
}

// IsEnabled returns whether heartbeat is enabled (defaults to false).
func (c HeartbeatConfig) IsEnabled() bool {
	return boolDefault(c.Enabled, false)
}

// Interval returns the configured heartbeat cadence.
func (c HeartbeatConfig) Interval() string {
	if c.Every == "" {
		return "10m"
	}
	return c.Every
}

// FilePath resolves the configured heartbeat file relative to the workspace.
func (c HeartbeatConfig) FilePath(workspace string) string {
	if c.File == "" {
		return filepath.Join(workspace, "HEARTBEAT.md")
	}
	if filepath.IsAbs(c.File) {
		return c.File
	}
	return filepath.Join(workspace, c.File)
}

// boolDefault dereferences a *bool pointer, returning def if the pointer is nil.
func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
