package config

// Snapshot is a read-only config snapshot assembled from DB for downstream
// consumption. It replaces the old *Config for code that needs provider/model
// information for a specific agent.
type Snapshot struct {
	Provider    string
	Model       string
	ModelStrong string
	ModelFast   string
	Workspace   string
	APIKey      string
	BaseURL     string
	Runner      RunnerConfig
	Compaction  CompactionConfig
	Heartbeat   HeartbeatConfig
	Scheduler   SchedulerConfig
	Plugins     []PluginConfig
}
