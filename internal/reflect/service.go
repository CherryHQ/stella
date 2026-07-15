package reflect

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour

	defaultMaxReviewTargetsPerAgent = 30
	defaultReflectRunSoftBudget     = 15 * time.Minute
)

// RuntimeMode selects the only Reflect writer that the scheduler may run.
// Keeping this boot-time-static prevents one target from being reviewed by
// legacy and structured writers during the same process lifetime.
type RuntimeMode string

const (
	RuntimeModeLegacy     RuntimeMode = "legacy"
	RuntimeModeStructured RuntimeMode = "structured"
)

func (m RuntimeMode) withDefault() RuntimeMode {
	if m == "" {
		return RuntimeModeLegacy
	}
	return m
}

func (m RuntimeMode) validate() error {
	switch m.withDefault() {
	case RuntimeModeLegacy, RuntimeModeStructured:
		return nil
	default:
		return fmt.Errorf("unsupported runtime mode %q", m)
	}
}

// skillWriteAuthorizer authorizes reflect's staged skill writes (create/patch/
// deprecate) under a fresh trusted WorkerAgentAuthority per operation. It is
// satisfied by *skillaccess.Service; when unset, reflect skill writes fail closed.
type skillWriteAuthorizer interface {
	AuthorizeWorkerWrite(ctx context.Context, userID, agentID, skillID string, create bool) error
}

// Config holds dependencies for the reflect service.
type Config struct {
	StateStore pkgplugins.StateStore
	Memory     memory.Provider
	Store      Store
	SkillStore pkgplugins.SkillStore
	// SkillAuthorizer applies Skill domain rules to reflect's staged writes (the
	// reconciliation-plan/usage-curator path); when nil those fail closed.
	// SkillReadAuthorizer gates the reviewer tool's DB-skill reads and
	// SkillToolWriteAuthorizer gates its prompt-driven create/patch/deprecate.
	SkillAuthorizer          skillWriteAuthorizer
	SkillReadAuthorizer      skillstool.SkillReadAuthorizer
	SkillToolWriteAuthorizer skillstool.SkillWriteAuthorizer
	UsageCuratorStore        UsageCuratorStore
	Notifier                 pkgplugins.Notifier
	Workspace                string
	Log                      *slog.Logger
	Providers                func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	CandidateGates           CandidateGateSettings
	// RuntimeMode selects legacy or structured review for the scheduler cycle.
	// It defaults to legacy during rollout.
	RuntimeMode RuntimeMode
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
	getLegacy(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
	getLine(ctx context.Context, sessionID string, line reflectLine) (reviewWatermark, error)
	setLine(ctx context.Context, sessionID string, line reflectLine, mark reviewWatermark) error
}

// Service runs background conversation review.
type Service struct {
	memory                   memory.Provider
	store                    Store
	skillStore               pkgplugins.SkillStore
	skillAuthorizer          skillWriteAuthorizer
	skillReadAuthz           skillstool.SkillReadAuthorizer
	skillToolWriteAuthz      skillstool.SkillWriteAuthorizer
	stateStore               pkgplugins.StateStore
	usageCuratorStore        UsageCuratorStore
	notifier                 pkgplugins.Notifier
	wm                       watermarker
	workspace                string
	maxReviewTargetsPerAgent int
	runSoftBudget            time.Duration
	now                      func() time.Time
	log                      *slog.Logger
	providers                func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	candidateGates           CandidateGateSettings
	usageCuratorSettings     UsageCuratorSettings
	runtimeMode              RuntimeMode
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
		skillAuthorizer:          cfg.SkillAuthorizer,
		skillReadAuthz:           cfg.SkillReadAuthorizer,
		skillToolWriteAuthz:      cfg.SkillToolWriteAuthorizer,
		stateStore:               cfg.StateStore,
		usageCuratorStore:        cfg.UsageCuratorStore,
		notifier:                 cfg.Notifier,
		wm:                       newWatermarkStore(cfg.StateStore),
		workspace:                cfg.Workspace,
		maxReviewTargetsPerAgent: defaultMaxReviewTargetsPerAgent,
		runSoftBudget:            defaultReflectRunSoftBudget,
		now:                      time.Now,
		log:                      cfg.Log,
		providers:                cfg.Providers,
		candidateGates:           cfg.CandidateGates.withDefaults(),
		usageCuratorSettings:     cfg.UsageCuratorSettings.withDefaults(),
		runtimeMode:              cfg.RuntimeMode.withDefault(),
		services:                 cfg.Services,
	}
}
