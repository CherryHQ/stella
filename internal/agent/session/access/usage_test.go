package access

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestUsageRequiresSessionReadAccessAndNeverReturnsPartialTotals(t *testing.T) {
	m := newSessionMatrix(t)
	q := sqlc.New(m.db)
	cost := pgtype.Numeric{}
	if err := cost.Scan("0.0125"); err != nil {
		t.Fatal(err)
	}
	rows := []sqlc.CreateAgentLLMCallParams{
		{
			ID: uuid.NewString(), SessionID: m.private, AgentID: m.agent, Provider: "provider-a", Model: "model-a", UsageReported: true,
			InputTokens: pgtype.Int8{Int64: 10, Valid: true}, OutputTokens: pgtype.Int8{Int64: 5, Valid: true},
			CacheReadTokens: pgtype.Int8{Int64: 3, Valid: true}, CacheWriteTokens: pgtype.Int8{Int64: 2, Valid: true}, CostUsd: cost,
			DurationMs: 10, OccurredAt: time.Now().UTC(),
		},
		{
			ID: uuid.NewString(), SessionID: m.private, AgentID: m.agent, Provider: "provider-a", Model: "model-a", UsageReported: false,
			DurationMs: 10, OccurredAt: time.Now().UTC(),
		},
	}
	for _, row := range rows {
		if _, err := q.CreateAgentLLMCall(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}

	owner, err := authz.NewUserAuthority(authz.UserID(m.owner), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := m.svc.Begin(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := access.Usage(t.Context(), m.agent, m.private)
	if err != nil {
		t.Fatal(err)
	}
	if usage.CallCount != 2 || usage.ReportedCallCount != 1 || usage.PricedCallCount != 1 {
		t.Fatalf("counts = %+v, want 2 calls / 1 reported / 1 priced", usage)
	}
	if usage.InputTokens != nil || usage.CostUSD != nil {
		t.Fatalf("partial totals must be null, got input=%v cost=%v", usage.InputTokens, usage.CostUSD)
	}
	if len(usage.Models) != 1 || usage.Models[0].InputTokens != nil || usage.Models[0].CostUSD != nil {
		t.Fatalf("model usage must not publish a partial total: %+v", usage.Models)
	}

	foreign, err := authz.NewUserAuthority(authz.UserID(m.other), false)
	if err != nil {
		t.Fatal(err)
	}
	foreignAccess, err := m.svc.Begin(t.Context(), foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignAccess.Usage(t.Context(), m.agent, m.private); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign usage read = %v, want opaque ErrNotFound", err)
	}
}

type usageProgressStub map[string]int64

func (s usageProgressStub) PendingCallCount(sessionID string) int64 { return s[sessionID] }

func TestUsageReportsAcceptedWritesStillPending(t *testing.T) {
	m := newSessionMatrix(t)
	m.svc.usage = usageProgressStub{m.private: 2}
	owner, err := authz.NewUserAuthority(authz.UserID(m.owner), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := m.svc.Begin(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := access.Usage(t.Context(), m.agent, m.private)
	if err != nil {
		t.Fatal(err)
	}
	if usage.PendingCallCount != 2 {
		t.Fatalf("pending calls = %d, want 2", usage.PendingCallCount)
	}
}
