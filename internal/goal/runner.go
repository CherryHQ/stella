package goal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// defaultStdoutLimit caps captured check stdout before it touches an
// acceptance_event detail (contract §4.1: "truncated to N KB"). 16 KiB keeps
// the typical build/test tail addressable inline without bloating the ledger;
// anything larger is the caller's job to externalize by hash.
const defaultStdoutLimit = 16 << 10

// checkExecTimeout bounds a single deterministic check's sandbox exec. A
// runaway command must not pin a worker; this is a backstop, not a budget.
const checkExecTimeout = 10 * time.Minute

// ErrNoSandbox is returned when a deterministic check cannot resolve a sandbox
// session to run in. A check with no place to run is an infrastructure fault,
// not an unmet acceptance — it never silently reads as a pass.
var ErrNoSandbox = errors.New("goal: no sandbox session for deterministic check")

// sandboxCheckRunner is the production CheckRunner (contract §4.1): it executes
// a deterministic item's command in the goal's sandbox, derives Pass from
// the exit code, captures+truncates stdout, and builds the §4.1 cache key
// (forced-miss when repo/env provenance is unavailable). It is PURE of lifecycle
// writes — it reads the cache-probe index but never appends events or touches
// acceptance_state; the service folds its CheckResult.
type sandboxCheckRunner struct {
	q           *sqlc.Queries // cache probe only (ProbeCheckCache)
	stdoutLimit int
}

// NewCheckRunner builds the sandbox-backed CheckRunner. q is used only for the
// read-only cache probe; stdoutLimit ≤ 0 falls back to the package default. The
// returned value satisfies the CheckRunner interface declared in service.go.
func NewCheckRunner(q *sqlc.Queries, stdoutLimit int) CheckRunner {
	if stdoutLimit <= 0 {
		stdoutLimit = defaultStdoutLimit
	}
	return &sandboxCheckRunner{q: q, stdoutLimit: stdoutLimit}
}

// Run executes one deterministic acceptance item and returns its CheckResult
// (contract §4.1). It first builds the cache key and, when the key is
// hit-eligible, probes for a prior pass — a hit short-circuits the sandbox exec.
// On a miss it runs the command in the goal's session, derives
// Pass = (exit == item.expectExit), and truncates stdout. It never writes
// lifecycle; the service folds the result into an acceptance_event.
func (r *sandboxCheckRunner) Run(ctx context.Context, item AcceptanceItem, env CheckEnv, sess sandbox.Session) (CheckResult, error) {
	if item.Kind != ItemDeterministic {
		return CheckResult{}, fmt.Errorf("%w: check runner got non-deterministic item %q", ErrInvalidContract, item.ID)
	}

	key := CacheKey(item, env)
	if hit, ok := r.probeCache(ctx, key); ok {
		return CheckResult{
			ItemID:   item.ID,
			ExitCode: int(hit.ExitCode.Int64),
			Pass:     true, // the probe only returns result='pass' rows
			CacheKey: key,
			CacheHit: true,
		}, nil
	}

	if sess == nil {
		return CheckResult{}, ErrNoSandbox
	}

	res, err := sess.Exec(ctx, item.Command, sandbox.ExecOptions{Timeout: checkExecTimeout})
	if err != nil {
		// A failed exec (sandbox/IO fault) is not a check fail — a check with no
		// usable result must never read as a pass or a definitive fail. Surface it
		// so the worker retries the attempt rather than recording a verdict.
		return CheckResult{}, fmt.Errorf("exec check %q: %w", item.ID, err)
	}

	return CheckResult{
		ItemID:   item.ID,
		ExitCode: res.ExitCode,
		Pass:     res.ExitCode == item.expectExit(),
		Stdout:   truncate(res.Stdout, r.stdoutLimit),
		CacheKey: key,
		CacheHit: false,
	}, nil
}

// probeCache reports a prior passing result for a hit-eligible cache key. A ""
// key (forced miss per §4.1 provenance rule) and a missing querier both bypass
// the probe; pgx.ErrNoRows is a clean miss, any other error degrades to a miss
// (a re-run is always safe — a false miss costs a re-run, a false hit ships
// broken work).
func (r *sandboxCheckRunner) probeCache(ctx context.Context, key string) (sqlc.AgentGoalAcceptanceEvent, bool) {
	if key == "" || r.q == nil {
		return sqlc.AgentGoalAcceptanceEvent{}, false
	}
	row, err := r.q.ProbeCheckCache(ctx, key)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Degrade an unexpected probe error to a miss: re-running the check is
			// always correct, trusting a stale/uncertain hit is not.
			_ = err
		}
		return sqlc.AgentGoalAcceptanceEvent{}, false
	}
	return row, true
}

// truncate caps s to limit bytes, marking elision so a downstream reader knows
// the tail was dropped. limit ≤ 0 means no cap.
func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	const mark = "\n…[truncated]"
	if limit <= len(mark) {
		return s[:limit]
	}
	return s[:limit-len(mark)] + mark
}
