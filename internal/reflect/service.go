package reflect

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour
)

// Config holds dependencies for the reflect service.
type Config struct {
	DB        *sql.DB
	Memory    memory.Provider
	Store     config.Store
	Notifier  *channel.Dispatcher
	Workspace string
	Interval  time.Duration
	Batch     int
	Log       *slog.Logger
	Providers func(api, apiKey, baseURL string) (*providers.Registry, error)
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	get(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
}

// Service runs background conversation review.
type Service struct {
	memory    memory.Provider
	store     config.Store
	notifier  *channel.Dispatcher
	wm        watermarker
	workspace string
	interval  time.Duration
	batch     int
	log       *slog.Logger
	providers func(api, apiKey, baseURL string) (*providers.Registry, error)
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 5
	}

	return &Service{
		memory:    cfg.Memory,
		store:     cfg.Store,
		notifier:  cfg.Notifier,
		wm:        newWatermarkStore(sqlc.New(cfg.DB)),
		workspace: cfg.Workspace,
		interval:  cfg.Interval,
		batch:     cfg.Batch,
		log:       cfg.Log,
		providers: cfg.Providers,
	}
}
