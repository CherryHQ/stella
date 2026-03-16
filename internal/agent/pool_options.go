package agent

import (
	"time"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/memory"
)

// PoolOption configures a Pool.
type PoolOption func(*Pool)

// WithAgentID sets the agent ID this pool belongs to.
func WithAgentID(id string) PoolOption {
	return func(p *Pool) {
		p.agentID = id
	}
}

// WithIdleTimeout sets the idle timeout for reaping runners.
func WithIdleTimeout(d time.Duration) PoolOption {
	return func(p *Pool) {
		p.idleTimeout = d
	}
}

// WithCompaction sets the compaction configuration.
func WithCompaction(cfg CompactionConfig) PoolOption {
	return func(p *Pool) {
		p.compaction = cfg
	}
}

// WithDefaultModel sets the default model ID for new runners.
func WithDefaultModel(model string) PoolOption {
	return func(p *Pool) {
		p.defaultModel = model
	}
}

// WithFastModel sets the model ID used for compaction and other fast tasks.
func WithFastModel(model string) PoolOption {
	return func(p *Pool) {
		p.fastModel = model
	}
}

// WithPluginHooks sets the plugin lifecycle hook runner.
func WithPluginHooks(hooks engine.PluginHookRunner) PoolOption {
	return func(p *Pool) {
		p.pluginHooks = hooks
	}
}

// WithUserMemory sets the per-user memory store for system prompt injection.
func WithUserMemory(store *memory.UserMemoryStore) PoolOption {
	return func(p *Pool) {
		p.userMemory = store
	}
}

// ChatOption configures a single Chat call.
type ChatOption func(*chatOptions)

type chatOptions struct {
	model string
}

// WithModel overrides the model for this Chat call. If the session already
// has a runner with a different model, the runner is replaced.
func WithModel(model string) ChatOption {
	return func(o *chatOptions) {
		o.model = model
	}
}
