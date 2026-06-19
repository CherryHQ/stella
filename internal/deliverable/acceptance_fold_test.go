package deliverable

import (
	"slices"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// This file tests the PURE acceptance fold DeriveAcceptance(contract,
// currentOutputHash, events) (acceptance.go, contract §4.2/§4.3). The fold is
// DB-free: tests build []sqlc.AgentDlvAcceptanceEvent literals directly. The
// fold trusts the query's seq-ascending order and folds in slice order, so the
// helpers below emit events in seq-ascending order.

// accf_currentHash is the canonical "current output" hash a fresh judgment
// verdict must anchor to so it is NOT stale (§4.2).
const accf_currentHash = "H-current"

// accf_detItem builds a deterministic contract item.
func accf_detItem(id string, required bool) AcceptanceItem {
	return AcceptanceItem{ID: id, Kind: ItemDeterministic, Required: required, Command: "true"}
}

// accf_judgItem builds a judgment contract item (human authority by default —
// authority is irrelevant to the fold's pass/fail bookkeeping; only Kind and
// Required gate, plus scope_hash staleness for judgment events).
func accf_judgItem(id string, required bool) AcceptanceItem {
	return AcceptanceItem{ID: id, Kind: ItemJudgment, Required: required, Authority: AuthorityHuman, Prompt: "ok?"}
}

// accf_detEvent builds a deterministic acceptance event. Deterministic events
// are always valid regardless of scope_hash (§4.2), so it is left empty.
func accf_detEvent(seq int64, itemID, result string) sqlc.AgentDlvAcceptanceEvent {
	return sqlc.AgentDlvAcceptanceEvent{
		Seq:       seq,
		ItemID:    itemID,
		ItemKind:  ItemDeterministic,
		Result:    result,
		Authority: AuthoritySystem,
	}
}

// accf_judgEvent builds a judgment acceptance event with an explicit scope_hash.
// A judgment event is valid only when scopeHash == currentOutputHash (§4.2).
func accf_judgEvent(seq int64, itemID, result, scopeHash string) sqlc.AgentDlvAcceptanceEvent {
	return sqlc.AgentDlvAcceptanceEvent{
		Seq:       seq,
		ItemID:    itemID,
		ItemKind:  ItemJudgment,
		Result:    result,
		Authority: AuthorityHuman,
		ScopeHash: scopeHash,
	}
}

// accf_hasPending reports whether itemID appears in the projection's pending set.
func accf_hasPending(p Projection, itemID string) bool {
	return slices.Contains(p.PendingItems, itemID)
}

// TestAcceptanceFold_TrivialContract: an empty (no items) contract is the
// auto-accept degradation — DeriveAcceptance returns passed unconditionally,
// ignoring any events (§4.3, acceptance.go IsTrivial branch).
func TestAcceptanceFold_TrivialContract(t *testing.T) {
	cases := []struct {
		name   string
		events []sqlc.AgentDlvAcceptanceEvent
	}{
		{"no events", nil},
		// Even a recorded fail event cannot drag a trivial contract off passed:
		// the fold short-circuits before any event inspection.
		{"with a fail event", []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "x", ResultFail)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveAcceptance(AcceptanceContract{}, "", tc.events)
			if got.State != AcceptancePassed {
				t.Fatalf("trivial contract State=%q want %q", got.State, AcceptancePassed)
			}
			if got.NeedsVerdict {
				t.Fatalf("trivial contract NeedsVerdict=true want false")
			}
			if len(got.PendingItems) != 0 {
				t.Fatalf("trivial contract PendingItems=%v want empty", got.PendingItems)
			}
		})
	}
}

// TestAcceptanceFold_DetThenJudgment_GatesOnVerdict (default policy): every
// required deterministic item passes and a required human-judgment item is
// pending => state pending with NeedsVerdict=true and the judgment item listed
// pending. This is the canonical "blocked(needs_verdict)" gate (§4.3,
// evalDetThenJudgment phase 2).
func TestAcceptanceFold_DetThenJudgment_GatesOnVerdict(t *testing.T) {
	c := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			accf_detItem("build", true),
			accf_judgItem("review", true),
		},
	}
	events := []sqlc.AgentDlvAcceptanceEvent{
		accf_detEvent(1, "build", ResultPass),
	}
	got := DeriveAcceptance(c, accf_currentHash, events)
	if got.State != AcceptancePending {
		t.Fatalf("State=%q want %q", got.State, AcceptancePending)
	}
	if !got.NeedsVerdict {
		t.Fatalf("NeedsVerdict=false want true (required human judgment pending)")
	}
	if !accf_hasPending(got, "review") {
		t.Fatalf("PendingItems=%v want to include %q", got.PendingItems, "review")
	}
	if accf_hasPending(got, "build") {
		t.Fatalf("PendingItems=%v should not include passed det item %q", got.PendingItems, "build")
	}
}

// TestAcceptanceFold_DetThenJudgment_DetPendingDefersJudgment: a required
// deterministic item not yet in defers any judgment evaluation — the fold
// returns pending WITHOUT setting NeedsVerdict, even though a judgment item is
// also unresolved (evalDetThenJudgment phase-1 short-circuit on detPending).
func TestAcceptanceFold_DetThenJudgment_DetPendingDefersJudgment(t *testing.T) {
	c := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			accf_detItem("build", true),
			accf_judgItem("review", true),
		},
	}
	got := DeriveAcceptance(c, accf_currentHash, nil)
	if got.State != AcceptancePending {
		t.Fatalf("State=%q want %q", got.State, AcceptancePending)
	}
	if got.NeedsVerdict {
		t.Fatalf("NeedsVerdict=true want false (deterministic phase not yet complete)")
	}
	if !accf_hasPending(got, "build") {
		t.Fatalf("PendingItems=%v want to include pending det item %q", got.PendingItems, "build")
	}
}

// TestAcceptanceFold_DetThenJudgment_DetFailShortCircuits: a required
// deterministic fail is decisive — state is failed and judgment is never
// evaluated (no NeedsVerdict), even with a pending required judgment item
// (evalDetThenJudgment "det fail is decisive").
func TestAcceptanceFold_DetThenJudgment_DetFailShortCircuits(t *testing.T) {
	c := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			accf_detItem("build", true),
			accf_judgItem("review", true),
		},
	}
	events := []sqlc.AgentDlvAcceptanceEvent{
		accf_detEvent(1, "build", ResultFail),
	}
	got := DeriveAcceptance(c, accf_currentHash, events)
	if got.State != AcceptanceFailed {
		t.Fatalf("State=%q want %q", got.State, AcceptanceFailed)
	}
	if got.NeedsVerdict {
		t.Fatalf("NeedsVerdict=true want false (det fail short-circuits before judgment)")
	}
}

// TestAcceptanceFold_VerdictStaleness exercises §4.2: a judgment PASS verdict
// counts only when its scope_hash == currentOutputHash; a stale verdict (hash
// mismatch, including the empty-hash case) is dropped and the item reads pending
// again. A deterministic event is valid regardless of scope_hash.
func TestAcceptanceFold_VerdictStaleness(t *testing.T) {
	c := humanJudgmentContract() // single required human judgment item id="review"

	t.Run("fresh pass counts", func(t *testing.T) {
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_judgEvent(1, "review", ResultPass, accf_currentHash),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePassed {
			t.Fatalf("State=%q want %q (fresh pass verdict)", got.State, AcceptancePassed)
		}
		if got.NeedsVerdict {
			t.Fatalf("NeedsVerdict=true want false (item satisfied)")
		}
	})

	t.Run("stale pass dropped => pending again", func(t *testing.T) {
		// Verdict anchored to a now-superseded output hash: stale, must be ignored.
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_judgEvent(1, "review", ResultPass, "H-old"),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePending {
			t.Fatalf("State=%q want %q (stale verdict dropped)", got.State, AcceptancePending)
		}
		if !got.NeedsVerdict {
			t.Fatalf("NeedsVerdict=false want true (item re-reads pending after staleness)")
		}
		if !accf_hasPending(got, "review") {
			t.Fatalf("PendingItems=%v want to include %q", got.PendingItems, "review")
		}
	})

	t.Run("empty scope_hash never matches non-empty current", func(t *testing.T) {
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_judgEvent(1, "review", ResultPass, ""),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePending {
			t.Fatalf("State=%q want %q (empty scope_hash is stale vs non-empty current)", got.State, AcceptancePending)
		}
		if !got.NeedsVerdict {
			t.Fatalf("NeedsVerdict=false want true")
		}
	})

	t.Run("stale verdict deletes a prior fresh outcome", func(t *testing.T) {
		// Seq-ascending: a fresh pass lands first, then a stale event for the same
		// item arrives later. §4.2 says the stale event DROPS the prior recorded
		// outcome — the item must read pending, not stay passed.
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_judgEvent(1, "review", ResultPass, accf_currentHash),
			accf_judgEvent(2, "review", ResultPass, "H-old"),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePending {
			t.Fatalf("State=%q want %q (later stale event drops prior valid outcome)", got.State, AcceptancePending)
		}
		if !got.NeedsVerdict {
			t.Fatalf("NeedsVerdict=false want true")
		}
	})
}

// TestAcceptanceFold_DeterministicValidRegardlessOfScopeHash: a deterministic
// event is taken as authoritative even with a mismatched/empty scope_hash —
// staleness applies ONLY to judgment events (§4.2, verdictValid guards only
// ItemJudgment).
func TestAcceptanceFold_DeterministicValidRegardlessOfScopeHash(t *testing.T) {
	c := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items:  []AcceptanceItem{accf_detItem("build", true)},
	}
	// Deterministic event carries a scope_hash that does NOT match current; it
	// must still count as a pass.
	events := []sqlc.AgentDlvAcceptanceEvent{
		{Seq: 1, ItemID: "build", ItemKind: ItemDeterministic, Result: ResultPass, Authority: AuthoritySystem, ScopeHash: "H-mismatch"},
	}
	got := DeriveAcceptance(c, accf_currentHash, events)
	if got.State != AcceptancePassed {
		t.Fatalf("State=%q want %q (deterministic event valid regardless of scope_hash)", got.State, AcceptancePassed)
	}
}

// TestAcceptanceFold_PolicyAll covers PolicyAll: passed iff every required item
// passes; one required fail => failed; a missing required item => pending (with
// NeedsVerdict only for a pending required judgment item).
func TestAcceptanceFold_PolicyAll(t *testing.T) {
	twoReq := AcceptanceContract{
		Policy: PolicyAll,
		Items: []AcceptanceItem{
			accf_detItem("a", true),
			accf_detItem("b", true),
		},
	}
	cases := []struct {
		name         string
		c            AcceptanceContract
		events       []sqlc.AgentDlvAcceptanceEvent
		wantState    string
		wantNeedsVer bool
	}{
		{
			name:      "all required pass => passed",
			c:         twoReq,
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultPass), accf_detEvent(2, "b", ResultPass)},
			wantState: AcceptancePassed,
		},
		{
			name:      "one required fail => failed",
			c:         twoReq,
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultPass), accf_detEvent(2, "b", ResultFail)},
			wantState: AcceptanceFailed,
		},
		{
			name:      "missing required => pending",
			c:         twoReq,
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultPass)},
			wantState: AcceptancePending,
		},
		{
			// PolicyAll surfaces a pending required JUDGMENT item as NeedsVerdict
			// (acceptance.go evalAll: pending judgment => NeedsVerdict=true).
			name: "pending required judgment => NeedsVerdict",
			c: AcceptanceContract{
				Policy: PolicyAll,
				Items:  []AcceptanceItem{accf_detItem("a", true), accf_judgItem("review", true)},
			},
			events:       []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultPass)},
			wantState:    AcceptancePending,
			wantNeedsVer: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveAcceptance(tc.c, accf_currentHash, tc.events)
			if got.State != tc.wantState {
				t.Fatalf("State=%q want %q", got.State, tc.wantState)
			}
			if got.NeedsVerdict != tc.wantNeedsVer {
				t.Fatalf("NeedsVerdict=%v want %v", got.NeedsVerdict, tc.wantNeedsVer)
			}
		})
	}
}

// TestAcceptanceFold_PolicyAny covers PolicyAny: passed as soon as any required
// item passes; all-required-fail => failed; otherwise pending.
func TestAcceptanceFold_PolicyAny(t *testing.T) {
	twoReq := AcceptanceContract{
		Policy: PolicyAny,
		Items: []AcceptanceItem{
			accf_detItem("a", true),
			accf_detItem("b", true),
		},
	}
	cases := []struct {
		name      string
		events    []sqlc.AgentDlvAcceptanceEvent
		wantState string
	}{
		{
			name:      "one required pass => passed",
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultFail), accf_detEvent(2, "b", ResultPass)},
			wantState: AcceptancePassed,
		},
		{
			name:      "all required fail => failed",
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultFail), accf_detEvent(2, "b", ResultFail)},
			wantState: AcceptanceFailed,
		},
		{
			// One fail recorded, the other still missing: a pending item could yet
			// pass, so it stays pending (evalAny: failed only when failed==required).
			name:      "one fail, one pending => pending",
			events:    []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "a", ResultFail)},
			wantState: AcceptancePending,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveAcceptance(twoReq, accf_currentHash, tc.events)
			if got.State != tc.wantState {
				t.Fatalf("State=%q want %q", got.State, tc.wantState)
			}
		})
	}
	t.Run("a pass clears pending and verdict bookkeeping", func(t *testing.T) {
		// A required judgment item is pending (would set NeedsVerdict) but another
		// required item already passed: evalAny short-circuits to passed and wipes
		// PendingItems/NeedsVerdict.
		c := AcceptanceContract{
			Policy: PolicyAny,
			Items:  []AcceptanceItem{accf_judgItem("review", true), accf_detItem("b", true)},
		}
		events := []sqlc.AgentDlvAcceptanceEvent{accf_detEvent(1, "b", ResultPass)}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePassed {
			t.Fatalf("State=%q want %q", got.State, AcceptancePassed)
		}
		if got.NeedsVerdict {
			t.Fatalf("NeedsVerdict=true want false (a pass clears the pending judgment)")
		}
		if len(got.PendingItems) != 0 {
			t.Fatalf("PendingItems=%v want empty after a pass", got.PendingItems)
		}
	})
}

// TestAcceptanceFold_NonRequiredNeverGates: advisory (Required=false) items
// never gate acceptance under any policy — a failing or pending advisory item
// cannot move the state off passed when all required items pass.
func TestAcceptanceFold_NonRequiredNeverGates(t *testing.T) {
	policies := []string{PolicyDetThenJudgment, PolicyAll, PolicyAny}
	for _, pol := range policies {
		t.Run(pol, func(t *testing.T) {
			c := AcceptanceContract{
				Policy: pol,
				Items: []AcceptanceItem{
					accf_detItem("req", true),
					accf_detItem("advisory_fail", false),
					accf_judgItem("advisory_pending", false),
				},
			}
			// Required passes; one advisory fails outright; one advisory judgment is
			// pending. None of the advisory items may gate.
			events := []sqlc.AgentDlvAcceptanceEvent{
				accf_detEvent(1, "req", ResultPass),
				accf_detEvent(2, "advisory_fail", ResultFail),
			}
			got := DeriveAcceptance(c, accf_currentHash, events)
			if got.State != AcceptancePassed {
				t.Fatalf("State=%q want %q (advisory items must not gate)", got.State, AcceptancePassed)
			}
			if got.NeedsVerdict {
				t.Fatalf("NeedsVerdict=true want false (advisory judgment must not gate)")
			}
			if accf_hasPending(got, "advisory_pending") {
				t.Fatalf("PendingItems=%v must not list advisory item", got.PendingItems)
			}
		})
	}
}

// TestAcceptanceFold_DuplicateItemSeqOverwrites: events arrive seq-ascending and
// a later event for the same item overwrites the earlier one (latestValidByItem
// fold). A fail→pass progression for one item yields passed; pass→fail yields
// failed.
func TestAcceptanceFold_DuplicateItemSeqOverwrites(t *testing.T) {
	c := AcceptanceContract{
		Policy: PolicyAll,
		Items:  []AcceptanceItem{accf_detItem("a", true)},
	}
	t.Run("later pass overwrites earlier fail", func(t *testing.T) {
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_detEvent(1, "a", ResultFail),
			accf_detEvent(2, "a", ResultPass),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptancePassed {
			t.Fatalf("State=%q want %q (later pass overwrites earlier fail)", got.State, AcceptancePassed)
		}
	})
	t.Run("later fail overwrites earlier pass", func(t *testing.T) {
		events := []sqlc.AgentDlvAcceptanceEvent{
			accf_detEvent(1, "a", ResultPass),
			accf_detEvent(2, "a", ResultFail),
		}
		got := DeriveAcceptance(c, accf_currentHash, events)
		if got.State != AcceptanceFailed {
			t.Fatalf("State=%q want %q (later fail overwrites earlier pass)", got.State, AcceptanceFailed)
		}
	})
}
