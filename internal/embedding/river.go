package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// BackfillQueue isolates embedding backfill from other River queues. One
	// worker per node: backfill is a single serialized drainer, so two never race.
	BackfillQueue = "stella_embedding_backfill"
	// maxBatchesPerFire bounds the work one backfill firing does so a large
	// initial corpus drains over a few ticks instead of monopolizing a worker.
	maxBatchesPerFire = 50
	// defaultInterval is how often the backfill periodic fires when BootConfig.Interval
	// is unset. Each firing re-reads config and no-ops when the lane is disabled.
	defaultInterval = time.Minute
	// defaultBatchSize is the indexer batch size when BootConfig.BatchSize is unset.
	defaultBatchSize = 128
)

// errNoRiverClient guards the periodic registration against a missing
// BindRiverClient call (a composition-root wiring bug, not a runtime condition).
var errNoRiverClient = errors.New("embedding: StartBackfill before BindRiverClient")

// ErrDisabled is returned by resolve when the lane is turned off or unconfigured
// (no API key). It is a control signal, not a failure: query and backfill paths
// treat it as "no semantic lane", not as an error to surface or retry.
var ErrDisabled = errors.New("embedding: lane disabled")

// Settings is the runtime embedding configuration the Service reads on every query
// and backfill pass, so changes made in the UI take effect without a restart.
type Settings struct {
	Enabled   bool
	Model     string // model id sent to the API
	Dim       int    // requested output dimension (0 = model native)
	APIKey    string
	BaseURL   string
	Normalize bool
}

// SettingsProvider supplies the current embedding configuration. The composition
// root backs it with the DB config store; the Service reads it live.
type SettingsProvider interface {
	EmbeddingSettings(ctx context.Context) (Settings, error)
}

// Service is the always-present bundle for the embedding lane. Unlike a boot-fixed
// provider, it is config-driven: it reads Settings on each use and (re)builds its
// chain + indexer when the configuration changes, so enabling/disabling the lane
// or swapping model/key/dimension in the UI takes effect at runtime. It does NOT
// own a River client; the composition root injects the shared one via BindRiverClient.
//
// When the lane is disabled the query embedder reports "no space" (search stays
// pure-BM25) and the backfill worker no-ops, so an unconfigured deployment behaves
// exactly as if no embedding existed — the worker just idles.
type Service struct {
	settings  SettingsProvider
	q         *sqlc.Queries
	batchSize int
	interval  time.Duration
	logger    *slog.Logger

	river *river.Client[pgx.Tx]

	// mu guards the cached build so concurrent queries reuse one chain and a config
	// change rebuilds it exactly once. It also guards the one-shot river bind.
	mu      sync.Mutex
	cached  *resolved
	started bool
}

// resolved is the chain + indexer built for one configuration fingerprint, reused
// across calls until the configuration changes.
type resolved struct {
	fingerprint string
	space       string
	normalize   bool
	chain       *Chain
	indexer     *Indexer
}

// BootConfig is the wiring the composition root supplies to build the lane.
type BootConfig struct {
	DB        *pgxpool.Pool
	Settings  SettingsProvider // live config source (DB-backed)
	BatchSize int              // indexer batch size (0 => defaultBatchSize)
	Interval  time.Duration    // backfill tick (0 => defaultInterval)
	Logger    *slog.Logger
}

// Boot constructs the always-present embedding lane. The backfill periodic is not
// started; the composition root injects the shared River client (BindRiverClient)
// and starts it (StartBackfill).
func Boot(cfg BootConfig) *Service {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "embedding")
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &Service{
		settings:  cfg.Settings,
		q:         sqlc.New(cfg.DB),
		batchSize: batchSize,
		interval:  interval,
		logger:    logger,
	}
}

// resolve returns the chain + indexer for the current configuration, building (or
// rebuilding on change) under the lock. ErrDisabled when the lane is off or has no
// API key.
func (s *Service) resolve(ctx context.Context) (*resolved, error) {
	cfg, err := s.settings.EmbeddingSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load embedding settings: %w", err)
	}
	if !cfg.Enabled || cfg.APIKey == "" {
		return nil, ErrDisabled
	}

	api := APIConfig{Model: cfg.Model, Dim: cfg.Dim, APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}
	// Fingerprint detects config changes that require rebuilding the chain. Hash it
	// rather than keep the tuple verbatim so the plaintext API key does not linger
	// in a long-lived in-memory string (heap dumps, panics). The key stays in the
	// hash input: rotating it must still change the fingerprint and force a rebuild.
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s\x00%t", cfg.Model, cfg.Dim, cfg.APIKey, cfg.BaseURL, cfg.Normalize))
	fp := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.cached.fingerprint == fp {
		return s.cached, nil
	}
	// space is the vector-space key (model id + requested dim): the indexer writes
	// it and the query lane filters on it, so both stay aligned with what the chain
	// stamps onto Result.Model (the provider's Model()).
	space := api.SpaceKey()
	chain := NewChain([]Provider{NewAPIProvider(api)}, BreakerConfig{}, nil)
	r := &resolved{
		fingerprint: fp,
		space:       space,
		normalize:   cfg.Normalize,
		chain:       chain,
		indexer: NewIndexer(s.q, chain, IndexConfig{
			Model:     space,
			Normalize: cfg.Normalize,
			BatchSize: s.batchSize,
		}),
	}
	s.cached = r
	return r, nil
}

// EmbedQuery embeds one search query into a storage-width vector and returns it
// with the vector space it belongs to, so the caller can run KNN against the
// matching index. When the lane is disabled it returns an empty space key and no
// error, which the caller treats as "no semantic lane" (pure-BM25 fallback).
// Normalization matches the writer's so query and document vectors are comparable.
func (s *Service) EmbedQuery(ctx context.Context, text string) (pgvector.Vector, string, error) {
	r, err := s.resolve(ctx)
	if errors.Is(err, ErrDisabled) {
		return pgvector.Vector{}, "", nil
	}
	if err != nil {
		return pgvector.Vector{}, "", err
	}
	res, err := r.chain.Embed(ctx, Request{Texts: []string{text}, Mode: ModeQuery})
	if err != nil {
		return pgvector.Vector{}, "", err
	}
	// Guard against a provider that returns success with no vector: degrade to a
	// search error (caller falls back to lexical) instead of panicking on res.Vectors[0].
	if len(res.Vectors) == 0 {
		return pgvector.Vector{}, "", errors.New("embedding: provider returned no vectors for query")
	}
	vec, err := ToStorageVector(res.Vectors[0], r.normalize)
	if err != nil {
		return pgvector.Vector{}, "", err
	}
	return pgvector.NewVector(vec), res.Model, nil
}

// backfillArgs is the River payload for a backfill firing. It carries nothing:
// the indexer reads the current backlog from the DB at work time.
type backfillArgs struct{}

// Kind implements river.JobArgs.
func (backfillArgs) Kind() string { return "stella_embedding_backfill" }

// backfillWorker drains the embedding backlog when a periodic fires. It resolves
// the current config each firing (so a disabled lane no-ops and a config change is
// picked up) and is fire-and-forget: errors are logged and retried on the next
// tick rather than surfaced to River, since the backlog is recomputed each pass.
type backfillWorker struct {
	river.WorkerDefaults[backfillArgs]
	svc *Service
	log *slog.Logger
}

// Work implements river.Worker.
func (w *backfillWorker) Work(ctx context.Context, _ *river.Job[backfillArgs]) error {
	r, err := w.svc.resolve(ctx)
	if errors.Is(err, ErrDisabled) {
		return nil // lane off: idle until enabled
	}
	if err != nil {
		w.log.Warn("embedding: backfill skipped, config unavailable", "error", err)
		return nil
	}
	for range maxBatchesPerFire {
		n, err := r.indexer.BackfillOnce(ctx)
		if err != nil {
			w.log.Warn("embedding: backfill pass failed, retrying next tick", "error", err)
			return nil
		}
		if n == 0 {
			break // backlog drained
		}
	}
	return nil
}

// BackfillQueueConfig returns the queue name and per-node worker config for the
// composition root assembling the shared working client.
func (s *Service) BackfillQueueConfig() (string, river.QueueConfig) {
	return BackfillQueue, river.QueueConfig{MaxWorkers: 1}
}

// RegisterRiverWorker registers the backfill worker into the shared workers
// bundle. Call before building the client (composition root).
func (s *Service) RegisterRiverWorker(workers *river.Workers) {
	river.AddWorker(workers, &backfillWorker{svc: s, log: s.logger.With("subcomponent", "river")})
}

// BindRiverClient injects the shared working River client the backfill periodic
// is registered against. Call after the client is built, before StartBackfill.
// BindRiverClient binds the shared working River client before StartBackfill.
// One-shot pre-start bind: rejects a nil client (missing), a second bind
// (duplicate), and any bind after StartBackfill (late).
func (s *Service) BindRiverClient(c *river.Client[pgx.Tx]) error {
	if c == nil {
		return errors.New("embedding: BindRiverClient requires a non-nil client")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("embedding: BindRiverClient after StartBackfill")
	}
	if s.river != nil {
		return errors.New("embedding: river client already bound")
	}
	s.river = c
	return nil
}

// StartBackfill registers the backfill as a single-leader River periodic job.
// River enqueues a periodic only on the elected leader and ByState uniqueness
// keeps at most one backfill in flight cluster-wide; RunOnStart kicks an initial
// drain so a fresh deployment starts populating immediately. Requires
// BindRiverClient first.
func (s *Service) StartBackfill() (rivertype.PeriodicJobHandle, error) {
	s.mu.Lock()
	if s.river == nil {
		s.mu.Unlock()
		return 0, errNoRiverClient
	}
	s.started = true
	s.mu.Unlock()
	handle := s.river.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(s.interval),
		func() (river.JobArgs, *river.InsertOpts) {
			return backfillArgs{}, &river.InsertOpts{
				Queue: BackfillQueue,
				UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
					rivertype.JobStateAvailable,
					rivertype.JobStatePending,
					rivertype.JobStateRunning,
					rivertype.JobStateScheduled,
					rivertype.JobStateRetryable,
				}},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))
	return handle, nil
}

// StopBackfill removes the backfill periodic so no further firings are enqueued.
func (s *Service) StopBackfill(handle rivertype.PeriodicJobHandle) {
	if s.river == nil {
		return
	}
	s.river.PeriodicJobs().Remove(handle)
}
