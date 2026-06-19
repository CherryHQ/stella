package goal

import (
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Judgment routing (contract §4.2). A judgment AcceptanceItem is resolved by a
// verdict — pass/fail evidence appended to the acceptance ledger, never a state
// write. Two authorities:
//
//   - authority=human: the goal blocks(needs_verdict); the human's decision
//     arrives via the API as a HumanVerdict the service folds in (HumanVerdictEvent).
//   - authority=agent: a reviewer-agent verdict folds in the same way via an
//     agent-authored acceptance_event. Producing that verdict (minting a
//     purpose=review attempt, parsing its output) is the deferred agent
//     auto-review path (contract §10.13); the fold already handles its events.
//
// Everything here is PURE event-param assembly. The acceptance fold and
// appendAcceptanceEvent are SERVICE calls that compose these helpers (the
// service owns all durable writes).

// HumanVerdict is the API-delivered human decision for an authority=human
// judgment item (contract §4.2). The service validates and folds it into an
// acceptance_event; it never sets acceptance_state directly.
type HumanVerdict struct {
	GoalID         string
	ItemID         string
	Pass           bool
	Rationale      string
	Scope          string
	ScopeHash      string // the accepted-output/artifact hash the verdict covers
	ReviewerUserID string
	AttemptID      string // the evaluated attempt whose output the verdict judges
}

// Valid reports whether a human verdict is well-formed. A verdict must name the
// item it answers and the reviewer who authored it; an empty scope_hash is
// allowed (it forces re-request on any subsequent output, the conservative
// default) but the identity fields are mandatory.
func (v HumanVerdict) Valid() bool {
	return v.GoalID != "" && v.ItemID != "" && v.ReviewerUserID != ""
}

// HumanVerdictEvent builds the append params for a human verdict (authority=human).
// seq/id/created_at are filled by appendAcceptanceEvent; this carries the
// verdict-as-evidence quartet (rationale/scope/authority/reviewer) + scope_hash.
func HumanVerdictEvent(v HumanVerdict, item AcceptanceItem) sqlc.AppendAcceptanceEventParams {
	return verdictEvent(verdictRow{
		goalID:         v.GoalID,
		attemptID:      v.AttemptID,
		itemID:         v.ItemID,
		pass:           v.Pass,
		authority:      AuthorityHuman,
		rationale:      v.Rationale,
		scope:          v.Scope,
		scopeHash:      v.ScopeHash,
		reviewerUserID: v.ReviewerUserID,
	})
}

// verdictRow is the shared input to verdictEvent for both authorities.
type verdictRow struct {
	goalID            string
	attemptID         string // the evaluated attempt the verdict judges
	itemID            string
	pass              bool
	authority         string
	rationale         string
	scope             string
	scopeHash         string
	reviewerUserID    string // authority=human
	reviewerAttemptID string // authority=agent
}

// verdictEvent assembles the AppendAcceptanceEventParams for a judgment verdict.
// A judgment row carries item_kind=judgment with a NULL exit_code (the schema's
// deterministic/judgment coupling CHECK); the result is pass/fail; the verdict
// quartet + scope_hash are first-class columns, not the detail blob.
func verdictEvent(r verdictRow) sqlc.AppendAcceptanceEventParams {
	return sqlc.AppendAcceptanceEventParams{
		GoalID:            r.goalID,
		AttemptID:         nullStr(r.attemptID),
		ItemID:            r.itemID,
		ItemKind:          ItemJudgment,
		Result:            verdictResult(r.pass),
		Authority:         r.authority,
		ReviewerUserID:    nullStr(r.reviewerUserID),
		ReviewerAttemptID: nullStr(r.reviewerAttemptID),
		Rationale:         r.rationale,
		Scope:             r.scope,
		ScopeHash:         r.scopeHash,
		Detail:            emptyJSON,
	}
}

// verdictResult maps a pass bool to the result enum value.
func verdictResult(pass bool) string {
	if pass {
		return ResultPass
	}
	return ResultFail
}
