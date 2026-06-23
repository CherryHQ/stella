package embedding

import (
	"context"
	"errors"
	"log/slog"
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
	// defaultInterval is how often backfill runs when BootConfig.Interval is unset.
	defaultInterval = time.Minute
)

// errNoRiverClient guards the periodic registration against a missing
// SetRiverClient call (a composition-root wiring bug, not a runtime condition).
var errNoRiverClient = errors.New("embedding: StartBackfill before SetRiverClient")

// Service is the boot-level bundle for the embedding lane. It owns the indexer
// (the backfill writer) and the chain (the query embedder), and contributes a
// backfill worker + periodic to the process-wide River client like the goal and
// scheduler subsystems. It does NOT own a River client; the composition root
// injects the shared one via SetRiverClient.
//
// The whole lane is opt-in: the composition root constructs a Service only when
// an embedding provider is configured, so an unconfigured deployment keeps the
// pure-BM25 behavior with no embedding worker and no query embedding.
type Service struct {
	chain    *Chain
	indexer  *Indexer
	model    string
	interval time.Duration
	logger   *slog.Logger

	river *river.Client[pgx.Tx]
}

// BootConfig is the wiring the composition root supplies to build the lane.
type BootConfig struct {
	DB        *pgxpool.Pool
	API       APIConfig // remote embedding provider; API.Model is the canonical space
	Normalize bool
	BatchSize int
	Interval  time.Duration // backfill tick (0 => defaultInterval)
	Logger    *slog.Logger
}

// Boot constructs the embedding lane: an API-first chain over the configured
// remote provider and an indexer that backfills the sidecars in that provider's
// space. The backfill periodic is not started; the composition root injects the
// shared River client (SetRiverClient) and starts it (StartBackfill).
func Boot(cfg BootConfig) *Service {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "embedding")
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	// space is the vector-space key (model id + requested dim): the indexer writes
	// it and the query lane filters on it, so both stay aligned with what the chain
	// stamps onto Result.Model (the provider's Model()).
	space := cfg.API.SpaceKey()
	chain := NewChain([]Provider{NewAPIProvider(cfg.API)}, BreakerConfig{}, nil)
	indexer := NewIndexer(sqlc.New(cfg.DB), chain, IndexConfig{
		Model:     space,
		Normalize: cfg.Normalize,
		BatchSize: cfg.BatchSize,
	})
	return &Service{
		chain:    chain,
		indexer:  indexer,
		model:    space,
		interval: interval,
		logger:   logger,
	}
}

// QueryModel is the vector-space key callers must filter on when searching with
// a vector this service produced.
func (s *Service) QueryModel() string { return s.model }

// EmbedQuery embeds one search query into a storage-width vector and returns it
// with the vector space it belongs to, so the caller can run KNN against the
// matching index. Normalization matches the writer's so query and document
// vectors are comparable.
func (s *Service) EmbedQuery(ctx context.Context, text string) (pgvector.Vector, string, error) {
	res, err := s.chain.Embed(ctx, Request{Texts: []string{text}, Mode: ModeQuery})
	if err != nil {
		return pgvector.Vector{}, "", err
	}
	// Guard against a provider that returns success with no vector: degrade to a
	// search error (caller falls back to lexical) instead of panicking on res.Vectors[0].
	if len(res.Vectors) == 0 {
		return pgvector.Vector{}, "", errors.New("embedding: provider returned no vectors for query")
	}
	vec, err := ToStorageVector(res.Vectors[0], s.indexer.cfg.Normalize)
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

// backfillWorker drains the embedding backlog when a periodic fires. It is
// fire-and-forget: errors are logged and retried on the next tick rather than
// surfaced to River, since the backlog is recomputed from the DB each pass.
type backfillWorker struct {
	river.WorkerDefaults[backfillArgs]
	ix  *Indexer
	log *slog.Logger
}

// Work implements river.Worker.
func (w *backfillWorker) Work(ctx context.Context, _ *river.Job[backfillArgs]) error {
	for range maxBatchesPerFire {
		n, err := w.ix.BackfillOnce(ctx)
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
	river.AddWorker(workers, &backfillWorker{ix: s.indexer, log: s.logger.With("subcomponent", "river")})
}

// SetRiverClient injects the shared working River client the backfill periodic
// is registered against. Call after the client is built, before StartBackfill.
func (s *Service) SetRiverClient(c *river.Client[pgx.Tx]) { s.river = c }

// StartBackfill registers the backfill as a single-leader River periodic job.
// River enqueues a periodic only on the elected leader and ByState uniqueness
// keeps at most one backfill in flight cluster-wide; RunOnStart kicks an initial
// drain so a fresh deployment starts populating immediately. Requires
// SetRiverClient first.
func (s *Service) StartBackfill() (rivertype.PeriodicJobHandle, error) {
	if s.river == nil {
		return 0, errNoRiverClient
	}
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
