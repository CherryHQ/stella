package agent

import (
	"time"

	"github.com/vaayne/anna/memory"
	"github.com/vaayne/anna/store"
)

// PoolOption configures a Pool.
type PoolOption func(*Pool)

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

// WithStore sets the persistent store for session history.
func WithStore(s store.Store) PoolOption {
	return func(p *Pool) {
		p.store = s
	}
}

// WithMemoryEngine sets the memory engine for message persistence and compaction.
// When set, the memory engine handles Ingest/Assemble/Compact; store.Store is
// still used for session metadata (SaveInfo, LoadInfo, ListInfo).
func WithMemoryEngine(engine memory.Engine) PoolOption {
	return func(p *Pool) {
		p.mem = engine
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
