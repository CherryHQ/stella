package reflect

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
)

// BuiltinJobName is the scheduler builtin name reflect registers under.
// Exported so gateway wiring and operational tooling refer to one source.
const BuiltinJobName = "reflect-review"

// NewBuiltinHandler returns a scheduler.OnJobFunc that runs one reflect
// review cycle per fire. Cycle-level failures (store errors, ctx cancellation)
// surface as errors so the scheduler marks the run errored. Per-agent
// failures are still logged inside the service and do not fail the run.
func NewBuiltinHandler(cfg Config) (scheduler.OnJobFunc, error) {
	if cfg.Memory == nil {
		return nil, fmt.Errorf("reflect: Memory provider is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("reflect: Store is required")
	}
	if cfg.StateStore == nil {
		return nil, fmt.Errorf("reflect: StateStore is required")
	}
	if cfg.Providers == nil {
		return nil, fmt.Errorf("reflect: Providers builder is required")
	}
	if err := cfg.RuntimeMode.validate(); err != nil {
		return nil, fmt.Errorf("reflect: %w", err)
	}
	if cfg.RuntimeMode.withDefault() == RuntimeModeStructured {
		if _, ok := cfg.Memory.(memory.FactStore); !ok {
			return nil, fmt.Errorf("reflect: Memory provider must support fact reads for structured mode")
		}
		if _, ok := memory.Unwrap(cfg.Memory).(factBatchWriter); !ok {
			return nil, fmt.Errorf("reflect: Memory provider must support fact batch writes for structured mode")
		}
		if _, ok := cfg.SkillStore.(skillRelatedBundleStore); !ok {
			return nil, fmt.Errorf("reflect: SkillStore must support related bundle reads for structured mode")
		}
		if _, ok := cfg.SkillStore.(reflectSkillWriter); !ok {
			return nil, fmt.Errorf("reflect: SkillStore must support reflect writes for structured mode")
		}
		if cfg.SkillAuthorizer == nil {
			return nil, fmt.Errorf("reflect: SkillAuthorizer is required for structured mode")
		}
	}
	if cfg.UsageCuratorSettings.withDefaults().Mode == UsageCuratorModeArmed {
		if cfg.UsageCuratorStore == nil {
			return nil, fmt.Errorf("reflect: UsageCuratorStore is required for armed usage curator")
		}
		// Armed curator performs actual lifecycle writes, so missing writer
		// capabilities should fail during wiring instead of partway through a run.
		if _, ok := memory.Unwrap(cfg.Memory).(factBatchWriter); !ok {
			return nil, fmt.Errorf("reflect: Memory provider must support fact batch writes for armed usage curator")
		}
		if _, ok := cfg.SkillStore.(usageCuratorSkillWriter); !ok {
			return nil, fmt.Errorf("reflect: SkillStore must support reflect skill deprecate for armed usage curator")
		}
		if cfg.SkillAuthorizer == nil {
			return nil, fmt.Errorf("reflect: SkillAuthorizer is required for armed usage curator")
		}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return func(ctx context.Context, job scheduler.Job) error {
		perFire := cfg
		return New(perFire).RunOnce(ctx)
	}, nil
}
