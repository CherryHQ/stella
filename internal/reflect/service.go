package reflect

import (
	"context"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour

	// defaultReviewBatch caps how many candidate sessions a single review
	// cycle processes per agent.
	defaultReviewBatch = 5
)

// Config holds dependencies for the reflect service.
type Config struct {
	StateStore pkgplugins.StateStore
	Memory     memory.Provider
	Store      Store
	SkillStore pkgplugins.SkillStore
	Notifier   pkgplugins.Notifier
	Workspace  string
	Log        *slog.Logger
	Providers  func(api, apiKey, baseURL string) (providers.StreamFunc, error)
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	get(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
}

// Service runs background conversation review.
type Service struct {
	memory     memory.Provider
	store      Store
	skillStore pkgplugins.SkillStore
	notifier   pkgplugins.Notifier
	wm         watermarker
	workspace  string
	batch      int
	log        *slog.Logger
	providers  func(api, apiKey, baseURL string) (providers.StreamFunc, error)
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{
		memory:     cfg.Memory,
		store:      cfg.Store,
		skillStore: cfg.SkillStore,
		notifier:   cfg.Notifier,
		wm:         newWatermarkStore(cfg.StateStore),
		workspace:  cfg.Workspace,
		batch:      defaultReviewBatch,
		log:        cfg.Log,
		providers:  cfg.Providers,
	}
}
