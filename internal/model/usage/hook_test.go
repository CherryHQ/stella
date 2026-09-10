package usage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	storepkg "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
)

func TestHookPersistsReportedUsageAndLeavesMissingUsageEmpty(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := storepkg.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "agent-1", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	for _, sessionID := range []string{"reported", "missing"} {
		if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID: uuid.NewString(), SessionID: sessionID, AgentID: pgtype.Text{String: "agent-1", Valid: true},
			UserID: pgtype.Text{String: uuid.NewString(), Valid: true}, Channel: "web", Kind: "chat", LastActive: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	h := New(db)
	h.OnPostLLMCall(ctx, &hooks.PostLLMCallContext{
		HookMeta: hooks.HookMeta{SessionID: "reported", AgentID: "agent-1"}, Provider: "provider", Model: "model",
		Usage:    ai.Usage{Reported: true, InputTokens: 10, OutputTokens: 5, CacheRead: 3, CacheWrite: 2, CostConfigured: true, Cost: ai.UsageCost{Total: 0.0125}},
		Duration: time.Second,
	})
	h.OnPostLLMCall(ctx, &hooks.PostLLMCallContext{
		HookMeta: hooks.HookMeta{SessionID: "missing", AgentID: "agent-1"}, Provider: "provider", Model: "model", Duration: time.Second,
	})

	var reported, missing struct {
		UsageReported bool
		Input         pgtype.Int8
		Cost          pgtype.Numeric
	}
	if err := db.QueryRow(ctx, `SELECT usage_reported, input_tokens, cost_usd FROM agent_llm_call WHERE session_id = 'reported'`).Scan(&reported.UsageReported, &reported.Input, &reported.Cost); err != nil {
		t.Fatal(err)
	}
	if !reported.UsageReported || !reported.Input.Valid || reported.Input.Int64 != 10 || !reported.Cost.Valid {
		t.Fatalf("reported row = %+v", reported)
	}
	if err := db.QueryRow(ctx, `SELECT usage_reported, input_tokens, cost_usd FROM agent_llm_call WHERE session_id = 'missing'`).Scan(&missing.UsageReported, &missing.Input, &missing.Cost); err != nil {
		t.Fatal(err)
	}
	if missing.UsageReported || missing.Input.Valid || missing.Cost.Valid {
		t.Fatalf("missing-usage row must be empty, got %+v", missing)
	}
}

func TestRunUsageSurvivesModelCancellationAndRejectsLateWrites(t *testing.T) {
	db := dbtest.New(t)
	if err := storepkg.NewDBStore(db).CreateAgent(t.Context(), config.Agent{ID: "agent", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7()).String()
	if _, err := sqlc.New(db).CreateConversation(t.Context(), sqlc.CreateConversationParams{ID: id, SessionID: id, Kind: "chat", LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ctx, lease, err := sessionexecution.New(db).Claim(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Finish("error") }()
	h := New(db)
	observation := &hooks.PostLLMCallContext{HookMeta: hooks.HookMeta{SessionID: id, AgentID: "agent"}, Provider: "test", Model: "test"}
	modelCtx, cancelModel := context.WithCancel(ctx)
	cancelModel()
	h.OnPostLLMCall(modelCtx, observation)
	var count int
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM agent_llm_call WHERE session_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("usage before finish=%d err=%v", count, err)
	}
	if err := lease.Finish("success"); err != nil {
		t.Fatal(err)
	}
	h.OnPostLLMCall(context.WithoutCancel(ctx), observation)
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM agent_llm_call WHERE session_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("late usage=%d err=%v", count, err)
	}
}
