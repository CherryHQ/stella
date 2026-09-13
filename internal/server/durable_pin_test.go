package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/agent"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/sessionevent"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// durableTurnFixture stands up the minimum real-PG graph a streamed turn
// needs: a conversation, a claimed execution lease, the event log the durable
// readers tail, and a Server wired to it.
type durableTurnFixture struct {
	db     *pgxpool.Pool
	server *Server
	events *sessionevent.Store
	execs  *sessionexecution.Store
	runs   *agentrun.Store
	attach sessionaccess.AttachResult
}

func newDurableTurnFixture(t *testing.T) (*durableTurnFixture, string) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	sessionID := uuid.Must(uuid.NewV7()).String()
	if _, err := sqlc.New(db).CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, Kind: "chat", LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfgstore.NewDBStore(db).CreateAgent(ctx, config.Agent{
		ID: "a1", Scope: config.AgentScopeSystem, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	f := &durableTurnFixture{
		db:     db,
		events: sessionevent.New(db),
		execs:  sessionexecution.New(db),
		runs:   agentrun.New(db),
		attach: sessionaccess.AttachResult{
			Cancel:               func() {},
			BeforeProtectedEvent: func(context.Context) error { return nil },
		},
	}
	f.server = &Server{
		sessionEvents: f.events,
		readiness:     newReadiness(ctx, fakePinger{}),
	}
	return f, sessionID
}

// claim registers its cleanup immediately — a leaked lease would hold the
// session's open execution row and shadow every later assertion.
func (f *durableTurnFixture) claim(t *testing.T, sessionID string) *sessionexecution.Lease {
	t.Helper()
	_, lease, err := f.execs.Claim(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	t.Cleanup(func() { _ = lease.Finish("error") })
	return lease
}

// startTurn claims an execution lease, enqueues a run, commits its start
// marker, then appends the given text events — the real durable path a web
// send produces, minus the model.
func (f *durableTurnFixture) startTurn(t *testing.T, sessionID string, texts ...string) (runID string, lease *sessionexecution.Lease) {
	t.Helper()
	ctx := context.Background()
	lease = f.claim(t, sessionID)
	run, _, err := f.runs.EnqueueDirect(ctx, agentrun.EnqueueParams{
		SessionID:  sessionID,
		AgentID:    "a1",
		RequestKey: agentrun.RequestKeyRequest(uuid.Must(uuid.NewV7()).String()),
		Actor:      agentrun.Actor{V: agentrun.EnvelopeVersion, Kind: "user", Platform: "web"},
		Input:      agentrun.Input{V: agentrun.EnvelopeVersion, Kind: "message", Text: "hi"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := f.events.TurnStart(ctx, sessionID, lease.Token(), run.ID); err != nil {
		t.Fatalf("turn start: %v", err)
	}
	for _, text := range texts {
		if err := f.events.Append(ctx, sessionID, lease.Token(), run.ID,
			agentruntime.EncodeEvent(agent.Event{Text: text})); err != nil {
			t.Fatalf("append %q: %v", text, err)
		}
	}
	return run.ID, lease
}

// finishRun moves the run to a terminal state through the real start/finish
// path a worker takes.
func (f *durableTurnFixture) finishRun(t *testing.T, runID string, state agentrun.State, code string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if ok, err := f.runs.Start(ctx, tx, runID, "worker-a"); err != nil || !ok {
		t.Fatalf("start: ok=%v err=%v", ok, err)
	}
	if ok, err := f.runs.Finish(ctx, tx, runID, "worker-a", state, code); err != nil || !ok {
		t.Fatalf("finish: ok=%v err=%v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// A pinned cursor ("runID:seq") must re-observe exactly that turn — even after
// it finished and a newer turn opened. The sequence: E1 streams past the
// client's cursor, finishes, E2 starts; a reconnect carrying E1:2 must read
// only E1's tail and terminal, never E2's events. The bounded context makes a
// regression that attaches to still-running E2 fail fast instead of hanging
// on the whole-package timeout.
func TestPinnedCursorReobservesTerminalRun(t *testing.T) {
	f, sessionID := newDurableTurnFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	run1, lease1 := f.startTurn(t, sessionID, "ALPHA", "BETA")
	f.finishRun(t, run1, agentrun.StateCompleted, "")
	if err := lease1.Finish("completed"); err != nil {
		t.Fatalf("finish lease: %v", err)
	}

	f.startTurn(t, sessionID, "GAMMA")

	// E1 events: turn_start=1, ALPHA=2, BETA=3. The client consumed through 2.
	w := httptest.NewRecorder()
	ok, truncated := f.server.streamDurableTurn(ctx, w, w, "a1", sessionID, f.attach, run1+":2")
	if !ok || truncated {
		t.Fatalf("pinned re-observe failed: ok=%v truncated=%v", ok, truncated)
	}
	body := w.Body.String()
	if strings.Contains(body, "ALPHA") {
		t.Fatalf("consumed prefix re-sent on pinned resume:\n%s", body)
	}
	if !strings.Contains(body, "BETA") {
		t.Fatalf("pinned resume lost E1's tail:\n%s", body)
	}
	if strings.Contains(body, "GAMMA") {
		t.Fatalf("pinned resume leaked E2's events:\n%s", body)
	}
	if !strings.Contains(body, `"scope":"`+run1+`"`) {
		t.Fatalf("terminal receipt did not name E1's scope:\n%s", body)
	}
}

// A recorded mid-turn Err row is not the turn's end: both durable readers must
// skip it and let the persisted terminal produce the wire verdict, so a replay
// from 0 reaches data-turn-terminal instead of dying at the error. Covers the
// run tail (verdict from agent_run state) and the execution tail (verdict from
// the lease's real turn_terminal marker).
func TestRecordedErrDoesNotCutTerminalReceipt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup writes the turn and returns the pin scope to re-observe.
		setup func(t *testing.T, f *durableTurnFixture, sessionID string) (pin string, wantReason string)
	}{
		{
			name: "run tail",
			setup: func(t *testing.T, f *durableTurnFixture, sessionID string) (string, string) {
				runID, lease := f.startTurn(t, sessionID, "ALPHA")
				if err := f.events.Append(context.Background(), sessionID, lease.Token(), runID,
					agentruntime.EncodeEvent(agent.Event{Err: errors.New("mid-turn boom")})); err != nil {
					t.Fatalf("append err event: %v", err)
				}
				f.finishRun(t, runID, agentrun.StateFailed, "model blew up")
				return runID + ":0", "model blew up"
			},
		},
		{
			name: "execution tail",
			setup: func(t *testing.T, f *durableTurnFixture, sessionID string) (string, string) {
				lease := f.claim(t, sessionID)
				if err := f.events.TurnStart(context.Background(), sessionID, lease.Token(), ""); err != nil {
					t.Fatalf("turn start: %v", err)
				}
				if err := f.events.Append(context.Background(), sessionID, lease.Token(), "",
					agentruntime.EncodeEvent(agent.Event{Text: "ALPHA"})); err != nil {
					t.Fatalf("append: %v", err)
				}
				if err := f.events.Append(context.Background(), sessionID, lease.Token(), "",
					agentruntime.EncodeEvent(agent.Event{Err: errors.New("mid-turn boom")})); err != nil {
					t.Fatalf("append err event: %v", err)
				}
				// The real finish writes the turn_terminal marker in the same
				// transaction that retires the lease row.
				if err := lease.Finish("failed"); err != nil {
					t.Fatalf("finish: %v", err)
				}
				return "x" + lease.Token() + ":0", "turn failed"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, sessionID := newDurableTurnFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pin, wantReason := tc.setup(t, f, sessionID)

			w := httptest.NewRecorder()
			ok, truncated := f.server.streamDurableTurn(ctx, w, w, "a1", sessionID, f.attach, pin)
			if !ok || truncated {
				t.Fatalf("stream failed: ok=%v truncated=%v", ok, truncated)
			}
			body := w.Body.String()
			if !strings.Contains(body, "ALPHA") {
				t.Fatalf("recorded Err cut the stream before replayed text:\n%s", body)
			}
			terminal := strings.Index(body, "data-turn-terminal")
			if terminal < 0 {
				t.Fatalf("terminal receipt never reached the wire:\n%s", body)
			}
			errFrame := strings.Index(body, `"type":"error"`)
			if errFrame < 0 || errFrame < terminal {
				t.Fatalf("failure verdict missing or ordered before the terminal receipt:\n%s", body)
			}
			if !strings.Contains(body, wantReason) {
				t.Fatalf("terminal reason %q not surfaced:\n%s", wantReason, body)
			}
		})
	}
}
