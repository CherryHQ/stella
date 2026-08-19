// Package llmusage persists provider-reported LLM usage outside the turn path.
package llmusage

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
)

const queueCapacity = 1024

// Hook accepts observations without waiting for PostgreSQL. The queue bounds
// shutdown loss to 1024 accepted records. Under sustained database overload,
// excess observations are deliberately dropped rather than delaying a turn.
type Hook struct {
	q   *sqlc.Queries
	log *slog.Logger

	jobs chan sqlc.CreateAgentLLMCallParams
	done chan struct{}
	wg   sync.WaitGroup

	mu     sync.RWMutex
	closed bool
}

func New(db *pgxpool.Pool) *Hook {
	return &Hook{
		q:    sqlc.New(db),
		log:  slog.With("hook", "llm_usage"),
		jobs: make(chan sqlc.CreateAgentLLMCallParams, queueCapacity),
		done: make(chan struct{}),
	}
}

func (*Hook) Name() string  { return "llm_usage" }
func (*Hook) Priority() int { return 10 }

// Start owns the one background writer for this process-lifetime core hook.
func (h *Hook) Start() {
	h.wg.Go(func() {
		for {
			select {
			case job := <-h.jobs:
				h.write(job)
			case <-h.done:
				for {
					select {
					case job := <-h.jobs:
						h.write(job)
					default:
						return
					}
				}
			}
		}
	})
}

func (h *Hook) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
	if hctx.SessionID == "" || hctx.AgentID == "" {
		return
	}
	job := paramsFrom(hctx)

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	select {
	case h.jobs <- job:
	default:
		h.log.Warn("llm usage queue full; dropping observation", "session_id", hctx.SessionID, "agent_id", hctx.AgentID)
	}
}

func paramsFrom(hctx *hooks.PostLLMCallContext) sqlc.CreateAgentLLMCallParams {
	p := sqlc.CreateAgentLLMCallParams{
		ID:            uuid.Must(uuid.NewV7()).String(),
		SessionID:     hctx.SessionID,
		AgentID:       hctx.AgentID,
		Provider:      hctx.Provider,
		Model:         hctx.Model,
		UsageReported: hctx.Usage.Reported,
		DurationMs:    hctx.Duration.Milliseconds(),
		StopReason:    string(hctx.StopReason),
		OccurredAt:    time.Now().UTC(),
	}
	if hctx.Error != nil {
		p.Error = hctx.Error.Error()
	}
	if hctx.TimeToFirstToken > 0 {
		p.TimeToFirstTokenMs = pgtype.Int8{Int64: hctx.TimeToFirstToken.Milliseconds(), Valid: true}
	}
	if !hctx.Usage.Reported {
		return p
	}
	p.InputTokens = pgtype.Int8{Int64: int64(hctx.Usage.InputTokens), Valid: true}
	p.OutputTokens = pgtype.Int8{Int64: int64(hctx.Usage.OutputTokens), Valid: true}
	p.CacheReadTokens = pgtype.Int8{Int64: int64(hctx.Usage.CacheRead), Valid: true}
	p.CacheWriteTokens = pgtype.Int8{Int64: int64(hctx.Usage.CacheWrite), Valid: true}
	// WithCost leaves Cost unconfigured when the model price is absent. That
	// state must stay NULL, while a known zero-token request is legitimately 0.
	if hctx.Usage.CostConfigured {
		_ = p.CostUsd.Scan(strconv.FormatFloat(hctx.Usage.Cost.Total, 'f', -1, 64))
	}
	return p
}

func (h *Hook) write(job sqlc.CreateAgentLLMCallParams) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.q.CreateAgentLLMCall(ctx, job); err != nil {
		h.log.Warn("persist llm usage", "error", err, "session_id", job.SessionID, "agent_id", job.AgentID)
	}
}

// Close stops admission, then drains the bounded queue before returning. It is
// called after agent runtimes stop, while the database pool is still available.
func (h *Hook) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.done)
	h.mu.Unlock()
	h.wg.Wait()
	return nil
}
