package reflect

import (
	"context"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	defaultMaxReviewTargetsPerAgent = 30
	defaultReflectRunSoftBudget     = 15 * time.Minute
)

// skillWriteAuthorizer authorizes reflect's staged skill writes (create/patch/
// delete) under a fresh trusted WorkerAgentAuthority per operation. It is
// satisfied by *skillaccess.Service; when unset, reflect skill writes fail closed.
type skillWriteAuthorizer interface {
	AuthorizeWorkerWrite(ctx context.Context, userID, agentID, skillID string, create bool) error
}

// Config holds dependencies for the reflect service.
type Config struct {
	StateStore pkgplugins.StateStore
	Memory     memory.Provider
	Store      Store
	// Snapshots loads per-agent Snapshots for review. The composition root passes
	// the credential-aware loader so reviews use each Agent's Provider key
	// overrides; when nil it falls back to Store, which also satisfies the loader.
	Snapshots  config.SnapshotLoader
	SkillStore pkgplugins.SkillStore
	// SkillAuthorizer applies Skill domain rules to reflect's staged writes (the
	// reconciliation-plan/usage-curator path); when nil those fail closed.
	SkillAuthorizer   skillWriteAuthorizer
	UsageCuratorStore UsageCuratorStore
	Log               *slog.Logger
	Providers         func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	CandidateGates    CandidateGateSettings
	// UsageCuratorSettings defaults to armed. Operators may switch to shadow to
	// stop lifecycle writes while keeping scans and telemetry active.
	UsageCuratorSettings UsageCuratorSettings
	// Services provides per-agent session registries for review target listing.
	// When set, reflect uses Registry.ListForReview and Registry.MemoryScope
	// instead of calling memory.SessionManager directly.
	Services agent.ServiceManager
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	getLine(ctx context.Context, sessionID string, line reflectLine) (reviewWatermark, error)
	setLine(ctx context.Context, sessionID string, line reflectLine, mark reviewWatermark) error
}

// Service runs background conversation review.
type Service struct {
	memory                   memory.Provider
	store                    Store
	snapshots                config.SnapshotLoader
	skillStore               pkgplugins.SkillStore
	skillAuthorizer          skillWriteAuthorizer
	stateStore               pkgplugins.StateStore
	usageCuratorStore        UsageCuratorStore
	wm                       watermarker
	maxReviewTargetsPerAgent int
	runSoftBudget            time.Duration
	now                      func() time.Time
	log                      *slog.Logger
	providers                func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	candidateGates           CandidateGateSettings
	usageCuratorSettings     UsageCuratorSettings
	services                 agent.ServiceManager
}

// snapshotLoader returns the configured credential-aware loader, falling back to
// store (which also satisfies config.SnapshotLoader) when unset — e.g. tests that
// construct Service directly rather than through New.
func (s *Service) snapshotLoader() config.SnapshotLoader {
	if s.snapshots != nil {
		return s.snapshots
	}
	return s.store
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	snapshots := cfg.Snapshots
	if snapshots == nil {
		// Store also satisfies config.SnapshotLoader, so an unset loader keeps the
		// prior undecorated behavior.
		snapshots = cfg.Store
	}
	return &Service{
		memory:                   cfg.Memory,
		store:                    cfg.Store,
		snapshots:                snapshots,
		skillStore:               cfg.SkillStore,
		skillAuthorizer:          cfg.SkillAuthorizer,
		stateStore:               cfg.StateStore,
		usageCuratorStore:        cfg.UsageCuratorStore,
		wm:                       newWatermarkStore(cfg.StateStore),
		maxReviewTargetsPerAgent: defaultMaxReviewTargetsPerAgent,
		runSoftBudget:            defaultReflectRunSoftBudget,
		now:                      time.Now,
		log:                      cfg.Log,
		providers:                cfg.Providers,
		candidateGates:           cfg.CandidateGates.withDefaults(),
		usageCuratorSettings:     cfg.UsageCuratorSettings.withDefaults(),
		services:                 cfg.Services,
	}
}
