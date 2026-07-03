package goal

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Projection is the derived acceptance view of a goal — the result of
// folding its acceptance ledger against its contract (contract §4.3). The
// service maps a Projection to exactly one lifecycle transition; nothing else
// produces a Projection or writes acceptance_state.
type Projection struct {
	State        string   // pending | passed | failed
	PendingItems []string // required items with no terminal (or only stale) event
	Gaps         Evaluation
	NeedsVerdict bool // a required human-authority item is pending/stale
}

// DeriveAcceptance is the PURE fold that turns events → projection (contract
// §4.3). It is the single place acceptance is decided. It takes the latest
// VALID event per item — a judgment event is valid only if its scope_hash
// matches currentOutputHash (§4.2 staleness); a deterministic event is taken as
// authoritative for its item. Policy evaluation then collapses item outcomes to
// a state. No DB calls, no clock, no side effects.
func DeriveAcceptance(c AcceptanceContract, currentOutputHash string, events []sqlc.AgentGoalAcceptanceEvent) Projection {
	// Trivial contract = auto-accept: nothing to evaluate, the goal's
	// acceptance is governed entirely by its children (composite) or is an
	// immediate pass (leaf). Treat as passed; derived rollup only calls the
	// composite accept path after all required children are done(accepted).
	if c.IsTrivial() {
		return Projection{State: AcceptancePassed}
	}

	latest := latestValidByItem(currentOutputHash, events)

	switch c.Policy {
	case PolicyAll:
		return evalAll(c, latest)
	case PolicyAny:
		return evalAny(c, latest)
	default: // PolicyDetThenJudgment is the default and most common
		return evalDetThenJudgment(c, latest)
	}
}

// PendingAgentReviewItems returns the required authority=agent judgment items
// that still have no VALID event against the current output — the items the
// agent auto-review producer must judge (contract §10.13). It is the same
// latest-valid-by-item fold acceptance uses, filtered to agent-authored
// judgment: an item with a stale verdict (scope_hash moved) reads as pending and
// is re-requested. A resolved item (valid pass OR fail) is excluded — the fold
// already accounts for it. Pure: no DB, no clock.
func PendingAgentReviewItems(c AcceptanceContract, currentOutputHash string, events []sqlc.AgentGoalAcceptanceEvent) []AcceptanceItem {
	if c.IsTrivial() {
		return nil
	}
	latest := latestValidByItem(currentOutputHash, events)
	var out []AcceptanceItem
	for _, it := range c.AgentJudgmentItems() {
		if _, ok := latest[it.ID]; !ok {
			out = append(out, it)
		}
	}
	return out
}

// itemOutcome is the salient outcome of the latest valid event for an item.
type itemOutcome struct {
	kind      string // ItemDeterministic | ItemJudgment
	authority string
	pass      bool
	gap       Gap
	present   bool
}

// latestValidByItem reduces the seq-ordered ledger to one outcome per item id,
// keeping only valid events. Events arrive in seq-ascending order (the query
// guarantees it), so a later event overwrites an earlier one for the same item.
// A judgment event is dropped when its scope_hash no longer matches the current
// output (stale verdict, §4.2); a deterministic event is always considered
// valid (its cache_key already bound it to its inputs at write time).
func latestValidByItem(currentOutputHash string, events []sqlc.AgentGoalAcceptanceEvent) map[string]itemOutcome {
	out := make(map[string]itemOutcome)
	for _, e := range events {
		if e.ItemKind == ItemJudgment && !verdictValid(e, currentOutputHash) {
			// A stale judgment verdict is ignored: re-request the item. Drop any
			// prior recorded outcome so the item reads as pending, not passed.
			delete(out, e.ItemID)
			continue
		}
		oc := itemOutcome{
			kind:      e.ItemKind,
			authority: e.Authority,
			pass:      e.Result == ResultPass,
			present:   true,
		}
		if !oc.pass {
			oc.gap = Gap{ItemID: e.ItemID, Reason: e.Rationale, Detail: e.Scope}
		}
		out[e.ItemID] = oc
	}
	return out
}

// verdictValid reports whether a judgment verdict still covers the current
// output. A verdict carries scope_hash = the artifact hash it covered at
// verdict time; it is valid only if that hash equals the current evaluated
// output hash (§4.2). An empty scope_hash never matches a non-empty current
// hash, forcing a re-request after the artifact moves.
func verdictValid(e sqlc.AgentGoalAcceptanceEvent, currentOutputHash string) bool {
	return e.ScopeHash == currentOutputHash
}

// evalAll: passed iff every required item passes; failed if any required item
// has a recorded fail (no rework path is decided here — the service applies
// budget); else pending.
func evalAll(c AcceptanceContract, latest map[string]itemOutcome) Projection {
	p := Projection{State: AcceptancePending}
	anyFail := false
	for _, it := range c.Items {
		if !it.Required {
			continue
		}
		oc, ok := latest[it.ID]
		switch {
		case !ok:
			p.PendingItems = append(p.PendingItems, it.ID)
			// Any pending required judgment item gates on a verdict. The fold parks
			// the goal NeedsVerdict regardless of authority; the dispatcher's
			// scanAndReview then mints an agent reviewer for authority=agent items
			// (contract §10.13) and falls back to a human verdict when none applies
			// or the review budget is spent.
			if it.Kind == ItemJudgment {
				p.NeedsVerdict = true
			}
		case oc.pass:
			// satisfied
		default:
			anyFail = true
			p.Gaps.Gaps = append(p.Gaps.Gaps, oc.gap)
		}
	}
	return finalize(p, anyFail)
}

// evalAny: passed iff at least one required item passes.
func evalAny(c AcceptanceContract, latest map[string]itemOutcome) Projection {
	p := Projection{State: AcceptancePending}
	var required, failed int
	for _, it := range c.Items {
		if !it.Required {
			continue
		}
		required++
		oc, ok := latest[it.ID]
		switch {
		case !ok:
			p.PendingItems = append(p.PendingItems, it.ID)
			// Any pending required judgment item gates on a verdict. The fold parks
			// the goal NeedsVerdict regardless of authority; the dispatcher's
			// scanAndReview then mints an agent reviewer for authority=agent items
			// (contract §10.13) and falls back to a human verdict when none applies
			// or the review budget is spent.
			if it.Kind == ItemJudgment {
				p.NeedsVerdict = true
			}
		case oc.pass:
			p.State = AcceptancePassed
			p.PendingItems = nil
			p.Gaps = Evaluation{}
			p.NeedsVerdict = false
			return p
		default:
			failed++
			p.Gaps.Gaps = append(p.Gaps.Gaps, oc.gap)
		}
	}
	// No pass yet. If every required item has resolved to a fail, it's failed;
	// otherwise still pending (a pending item could yet pass).
	if required > 0 && failed == required {
		p.State = AcceptanceFailed
	}
	return p
}

// evalDetThenJudgment evaluates deterministic required items first; any det
// fail short-circuits to failed (judgment never runs — cost discipline). If all
// deterministic items pass, evaluate judgment items: a missing/stale required
// judgment marks NeedsVerdict.
func evalDetThenJudgment(c AcceptanceContract, latest map[string]itemOutcome) Projection {
	p := Projection{State: AcceptancePending}

	// Phase 1: deterministic.
	detPending := false
	for _, it := range c.Items {
		if it.Kind != ItemDeterministic || !it.Required {
			continue
		}
		oc, ok := latest[it.ID]
		switch {
		case !ok:
			detPending = true
			p.PendingItems = append(p.PendingItems, it.ID)
		case oc.pass:
			// satisfied
		default:
			// A deterministic fail is decisive: short-circuit, skip judgment.
			p.State = AcceptanceFailed
			p.Gaps.Gaps = append(p.Gaps.Gaps, oc.gap)
			return p
		}
	}
	if detPending {
		// Deterministic checks not all in yet: wait before any judgment.
		return p
	}

	// Phase 2: judgment (all required deterministic items passed).
	anyFail := false
	for _, it := range c.Items {
		if it.Kind != ItemJudgment || !it.Required {
			continue
		}
		oc, ok := latest[it.ID]
		switch {
		case !ok:
			p.PendingItems = append(p.PendingItems, it.ID)
			p.NeedsVerdict = true
		case oc.pass:
			// satisfied
		default:
			anyFail = true
			p.Gaps.Gaps = append(p.Gaps.Gaps, oc.gap)
		}
	}
	return finalize(p, anyFail)
}

// finalize collapses an all/det-then-judgment phase to a terminal state:
// failed wins over pending; a clean sweep with nothing pending is passed.
// NeedsVerdict keeps the state pending (the service routes to agent-review or
// blocked(needs_verdict)).
func finalize(p Projection, anyFail bool) Projection {
	if anyFail {
		p.State = AcceptanceFailed
		return p
	}
	if len(p.PendingItems) == 0 {
		p.State = AcceptancePassed
	}
	return p
}

// CheckEnv carries the provenance inputs the cache key folds (contract §4.1).
// RepoTreeHash/EnvHash may be "" when the sandbox cannot guarantee a stable
// hash; that forces a cache miss rather than risking a false hit.
type CheckEnv struct {
	GoalID         string
	SandboxImage   string
	RepoTreeHash   string   // "" ⇒ forced miss
	EnvHash        string   // "" ⇒ forced miss
	UpstreamHashes []string // accepted-output hashes of upstream edges
}

// CheckResult is the deterministic-check outcome the runner returns; the
// service folds it into an acceptance_event (contract §4.1). It never writes
// lifecycle.
type CheckResult struct {
	ItemID   string
	ExitCode int
	Pass     bool
	Stdout   string
	CacheKey string
	CacheHit bool
}

// CacheKey is the single constructor for a deterministic check's cache key
// (contract §4.1, invariant #9). It folds item id+command, sandbox image, repo
// tree hash, env hash, and the sorted upstream accepted-output hashes. If
// RepoTreeHash OR EnvHash is unavailable ("") the key is "" — a forced miss
// that is never written as a hit-eligible row. A false miss costs a re-run; a
// false hit ships broken work.
func CacheKey(item AcceptanceItem, env CheckEnv) string {
	if env.RepoTreeHash == "" || env.EnvHash == "" {
		return ""
	}
	h := sha256.New()
	writeNUL(h, item.ID, item.Command, env.SandboxImage, env.RepoTreeHash, env.EnvHash)
	ups := append([]string(nil), env.UpstreamHashes...)
	sort.Strings(ups)
	for _, u := range ups {
		writeNUL(h, u)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeNUL writes each value followed by a NUL byte so concatenated fields
// cannot collide across boundaries.
func writeNUL(h interface{ Write([]byte) (int, error) }, vals ...string) {
	for _, v := range vals {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
}
