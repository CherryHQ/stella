package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/caarlos0/env/v11"
	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for anna.
// Env vars use ANNA_ prefix (e.g. ANNA_PROVIDER, ANNA_MODEL).
type Config struct {
	Provider    string                    `yaml:"provider"     env:"PROVIDER"`
	Model       string                    `yaml:"model"        env:"MODEL"`
	ModelStrong string                    `yaml:"model_strong" env:"MODEL_STRONG"`
	ModelFast   string                    `yaml:"model_fast"   env:"MODEL_FAST"`
	Workspace   string                    `yaml:"workspace"    env:"WORKSPACE"`
	Runner      RunnerConfig              `yaml:"runner"       envPrefix:"RUNNER_"`
	Scheduler   SchedulerConfig           `yaml:"scheduler"    envPrefix:"SCHEDULER_"`
	Heartbeat   HeartbeatConfig           `yaml:"heartbeat"    envPrefix:"HEARTBEAT_"`
	Providers   map[string]ProviderConfig `yaml:"providers"`
	Channels    ChannelsConfig            `yaml:"channels"`
	Plugins     []PluginConfig            `yaml:"plugins"`
}

// PluginConfig describes a single plugin entry in config.yaml.
type PluginConfig struct {
	Path   string         `yaml:"path"`
	Config map[string]any `yaml:"config"`
}

type RunnerConfig struct {
	Type        string           `yaml:"type"         env:"TYPE"`
	System      string           `yaml:"system"       env:"SYSTEM"`
	IdleTimeout int              `yaml:"idle_timeout" env:"IDLE_TIMEOUT"`
	Compaction  CompactionConfig `yaml:"compaction"   envPrefix:"COMPACTION_"`
}

// CompactionConfig controls automatic session compaction.
type CompactionConfig struct {
	// MaxTokens triggers compaction when the estimated token count exceeds this.
	// 0 (or omitted) uses the default of 80000. Negative values disable
	// automatic compaction. Manual /compact still works.
	MaxTokens int `yaml:"max_tokens" env:"MAX_TOKENS"`
	// KeepTail is the number of recent message entries to preserve verbatim
	// after compaction. Default: 20.
	KeepTail int `yaml:"keep_tail" env:"KEEP_TAIL"`
}

type SchedulerConfig struct {
	Enabled *bool  `yaml:"enabled"  env:"ENABLED"`
	DataDir string `yaml:"data_dir" env:"DATA_DIR"`
}

// IsEnabled returns whether the scheduler is enabled (defaults to true).
func (c SchedulerConfig) IsEnabled() bool {
	return boolDefault(c.Enabled, true)
}

type HeartbeatConfig struct {
	Enabled *bool  `yaml:"enabled" env:"ENABLED"`
	Every   string `yaml:"every"   env:"EVERY"`
	File    string `yaml:"file"    env:"FILE"`
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

type ProviderConfig struct {
	APIKey  string        `yaml:"api_key"`
	BaseURL string        `yaml:"base_url"`
	Models  []ModelConfig `yaml:"models"`
}

// Load loads config from the default anna home (~/.anna/config.yaml).
func Load() (*Config, error) {
	return LoadFrom(AnnaHome())
}

// LoadFrom loads config from the given directory.
func LoadFrom(dir string) (*Config, error) {
	cfg := &Config{}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	// Resolve workspace early so state.yaml can be found.
	// Priority: ANNA_WORKSPACE env -> yaml -> default.
	if cfg.Workspace == "" {
		if v := os.Getenv("ANNA_WORKSPACE"); v != "" {
			cfg.Workspace = v
		} else {
			cfg.Workspace = filepath.Join(dir, "workspace")
		}
	}

	// Apply runtime state overrides (state.yaml) — mutable values like
	// current provider/model set by "anna models set" or /model command.
	applyState(cfg)

	// Apply environment variable overrides (ANNA_ prefix).
	// Uses caarlos0/env struct tags; only set env vars override YAML values.
	if err := env.ParseWithOptions(cfg, env.Options{Prefix: "ANNA_"}); err != nil {
		return nil, fmt.Errorf("parse env vars: %w", err)
	}

	// Initialize providers map if nil.
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}

	// Resolve provider env vars for known providers.
	// These use standard env var names (ANTHROPIC_API_KEY, etc.) not ANNA_ prefix.
	resolveProviderEnv(cfg, "anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL")
	resolveProviderEnv(cfg, "openai", "OPENAI_API_KEY", "OPENAI_BASE_URL")
	resolveProviderEnv(cfg, "openai-response", "OPENAI_API_KEY", "OPENAI_BASE_URL")

	// Apply defaults for missing values.
	if cfg.Provider == "" {
		cfg.Provider = "anthropic"
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	if cfg.Runner.Type == "" {
		cfg.Runner.Type = "go"
	}
	if cfg.Runner.IdleTimeout == 0 {
		cfg.Runner.IdleTimeout = 10
	}
	if cfg.Scheduler.DataDir == "" {
		cfg.Scheduler.DataDir = filepath.Join(cfg.Workspace, "scheduler")
	}

	return cfg, nil
}

// resolveProviderEnv fills in api_key and base_url from environment variables
// if not already set in the config.
func resolveProviderEnv(cfg *Config, name, keyEnv, urlEnv string) {
	p := cfg.Providers[name]
	if p.APIKey == "" {
		if v := os.Getenv(keyEnv); v != "" {
			p.APIKey = v
		}
	}
	if p.BaseURL == "" {
		if v := os.Getenv(urlEnv); v != "" {
			p.BaseURL = v
		}
	}
	cfg.Providers[name] = p
}
