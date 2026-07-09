package reflect

import (
	"context"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour

	// defaultReviewBatch caps how many review targets a single cycle processes
	// per in-memory batch.
	defaultReviewBatch = 10

	defaultMaxReviewTargetsPerAgent = 30
)

// Config holds dependencies for the reflect service.
type Config struct {
	StateStore        pkgplugins.StateStore
	Memory            memory.Provider
	Store             Store
	SkillStore        pkgplugins.SkillStore
	UsageCuratorStore UsageCuratorStore
	Notifier          pkgplugins.Notifier
	Workspace         string
	Log               *slog.Logger
	Providers         func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	CandidateGates    CandidateGateSettings
	// UsageCuratorSettings defaults to shadow mode. Production may enable armed
	// mode explicitly via host wiring; restore remains an internal/admin path.
	UsageCuratorSettings UsageCuratorSettings
	// Services provides per-agent session registries for review target listing.
	// When set, reflect uses Registry.ListForReview and Registry.MemoryScope
	// instead of calling memory.SessionManager directly.
	Services agent.ServiceManager
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	get(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
	getLine(ctx context.Context, sessionID string, line reflectLine) (reviewWatermark, error)
	setLine(ctx context.Context, sessionID string, line reflectLine, mark reviewWatermark) error
}

// Service runs background conversation review.
type Service struct {
	memory                   memory.Provider
	store                    Store
	skillStore               pkgplugins.SkillStore
	stateStore               pkgplugins.StateStore
	usageCuratorStore        UsageCuratorStore
	notifier                 pkgplugins.Notifier
	wm                       watermarker
	workspace                string
	batch                    int
	maxReviewTargetsPerAgent int
	log                      *slog.Logger
	providers                func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	candidateGates           CandidateGateSettings
	usageCuratorSettings     UsageCuratorSettings
	services                 agent.ServiceManager
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{
		memory:                   cfg.Memory,
		store:                    cfg.Store,
		skillStore:               cfg.SkillStore,
		stateStore:               cfg.StateStore,
		usageCuratorStore:        cfg.UsageCuratorStore,
		notifier:                 cfg.Notifier,
		wm:                       newWatermarkStore(cfg.StateStore),
		workspace:                cfg.Workspace,
		batch:                    defaultReviewBatch,
		maxReviewTargetsPerAgent: defaultMaxReviewTargetsPerAgent,
		log:                      cfg.Log,
		providers:                cfg.Providers,
		candidateGates:           cfg.CandidateGates.withDefaults(),
		usageCuratorSettings:     cfg.UsageCuratorSettings.withDefaults(),
		services:                 cfg.Services,
	}
}
