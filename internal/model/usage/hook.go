// Package usage persists provider-reported usage synchronously inside the Run.
package usage

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/internal/sessionexecution"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// Hook persists usage synchronously under the current execution's write guard.
type Hook struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

func New(db *pgxpool.Pool) *Hook {
	return &Hook{
		db:  db,
		log: slog.With("hook", "llm_usage"),
	}
}

func (*Hook) Name() string  { return "llm_usage" }
func (*Hook) Priority() int { return 10 }

func (h *Hook) OnPostLLMCall(ctx context.Context, hctx *hooks.PostLLMCallContext) {
	if hctx.SessionID == "" || hctx.AgentID == "" {
		return
	}
	job := paramsFrom(hctx)
	// A model deadline does not discard its usage; the execution guard still
	// rejects writes after the owning Run is canceled or loses its lease.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionexecution.OperationTimeout)
	defer cancel()
	_, err := sessionexecution.Write(ctx, h.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.AgentLlmCall, error) {
		return q.CreateAgentLLMCall(ctx, job)
	})
	if err != nil {
		sessionexecution.Abort(ctx, err)
		h.log.Warn("persist run llm usage", "session_id", job.SessionID, "error", err)
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
	p.ReasoningTokens = pgtype.Int8{Int64: int64(max(hctx.Usage.ReasoningTokens, 0)), Valid: true}
	p.CacheReadTokens = pgtype.Int8{Int64: int64(hctx.Usage.CacheRead), Valid: true}
	p.CacheWriteTokens = pgtype.Int8{Int64: int64(hctx.Usage.CacheWrite), Valid: true}
	// WithCost leaves Cost unconfigured when the model price is absent. That
	// state must stay NULL, while a known zero-token request is legitimately 0.
	if hctx.Usage.CostConfigured {
		_ = p.CostUsd.Scan(strconv.FormatFloat(hctx.Usage.Cost.Total, 'f', -1, 64))
	}
	return p
}
