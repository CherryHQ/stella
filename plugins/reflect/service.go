package reflect

import (
	"context"
	"log/slog"
	"time"

	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour
)

// Config holds dependencies for the reflect service.
type Config struct {
	StateStore pkgplugins.PluginStateStore
	Memory     memory.Provider
	Store      pkgplugins.ReflectStore
	Notifier   pkgplugins.NotificationService
	Workspace  string
	Interval   time.Duration
	Batch      int
	Log        *slog.Logger
	Providers  func(api, apiKey, baseURL string) (*providers.Registry, error)
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	get(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
}

// Service runs background conversation review.
type Service struct {
	memory    memory.Provider
	store     pkgplugins.ReflectStore
	notifier  pkgplugins.NotificationService
	wm        watermarker
	workspace string
	interval  time.Duration
	batch     int
	log       *slog.Logger
	providers func(api, apiKey, baseURL string) (*providers.Registry, error)
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	cfg = normalizeConfig(cfg)
	return &Service{
		memory:    cfg.Memory,
		store:     cfg.Store,
		notifier:  cfg.Notifier,
		wm:        newWatermarkStore(cfg.StateStore),
		workspace: cfg.Workspace,
		interval:  cfg.Interval,
		batch:     cfg.Batch,
		log:       cfg.Log,
		providers: cfg.Providers,
	}
}

func normalizeConfig(cfg Config) Config {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 5
	}
	return cfg
}
