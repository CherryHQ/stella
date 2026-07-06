package goal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
)

// AttemptInput is the frozen-at-mint context an attempt executes against
// (contract §3.3). Marshaled to the attempt's input_context column. Frozen
// means an in-flight edit to intent/contract never mutates a running attempt.
type AttemptInput struct {
	Title           string                 `json:"title,omitempty"`
	Intent          string                 `json:"intent"`
	Context         json.RawMessage        `json:"context,omitempty"`
	UpstreamOutputs []AcceptedOutput       `json:"upstream_outputs"`           // ONLY accepted upstream outputs
	PriorGaps       *Evaluation            `json:"prior_gaps,omitempty"`       // Evaluation[i-1].gaps; nil on attempt 1
	PriorErrors     []ValidationError      `json:"prior_errors,omitempty"`     // structural planner errors from the previous repair turn
	Contract        AcceptanceContract     `json:"contract"`                   // the bar the attempt must clear
	ResolvedVerdict string                 `json:"resolved_verdict,omitempty"` // a human answer that unblocked needs_verdict
	TimelineContext []TimelineContextEvent `json:"timeline_context,omitempty"` // recent timeline facts: failures, gaps, human instructions
	AttemptNo       int                    `json:"attempt_no"`
	// MaxDepth is the root's recursion ceiling, frozen here so a decomposition
	// attempt can validate its proposed plan in-turn (against parentDepth =
	// goal.Depth) instead of only out-of-turn at SubmitDecomposition. Zero for
	// non-decomposition attempts, which never decompose.
	MaxDepth int `json:"max_depth,omitempty"`
	// ReviewItems / ReviewOutput / ReviewedAttemptID are frozen onto a
	// purpose=review attempt: the agent-authority judgment items the reviewer must
	// answer, the execution output it judges, and the execution attempt that
	// produced that output (contract §10.13). The reviewed id+hash pin the episode
	// — SubmitReview binds verdicts to this output and refuses to fold if the
	// evaluated output has moved since mint. Empty for non-review attempts.
	ReviewItems       []AcceptanceItem `json:"review_items,omitempty"`
	ReviewOutput      *AttemptOutput   `json:"review_output,omitempty"`
	ReviewedAttemptID string           `json:"reviewed_attempt_id,omitempty"`
}

// Evaluation carries the shortfalls from one acceptance fold; it feeds the next
// attempt's input as prior_gaps.
type Evaluation struct {
	Gaps []Gap `json:"gaps"`
}

// Gap is one unmet item with a reason fed into attempt_no+1.
type Gap struct {
	ItemID string `json:"item_id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// AttemptEvidence is the submitted handoff (contract §3.4). A non-root
// goal submitting an empty Summary is a retryable protocol miss.
type AttemptEvidence struct {
	Summary   string         `json:"summary"`
	Artifacts []ArtifactRef  `json:"artifacts,omitempty"`
	Notes     map[string]any `json:"notes,omitempty"`
}

// AttemptOutput is the candidate the contract evaluates (contract §3.4). Hash
// becomes accepted_output.Hash on acceptance.
type AttemptOutput struct {
	Result  map[string]any `json:"result,omitempty"`
	Summary string         `json:"summary"`
	Hash    string         `json:"hash"`
}

// ArtifactRef is a hash-addressed externalized artifact (stdout/diffs/files).
type ArtifactRef struct {
	Hash  string `json:"hash"`
	Kind  string `json:"kind"`
	URI   string `json:"uri,omitempty"`
	Bytes int64  `json:"bytes"`
}

// AcceptedOutput is the frozen snapshot copied from the accepted attempt
// (contract §3.5), then immutable. This is what flows downstream and what a
// verdict's scope_hash anchors to.
type AcceptedOutput struct {
	GoalID        string         `json:"goal_id"`
	Summary       string         `json:"summary"`
	Result        map[string]any `json:"result,omitempty"`
	Artifacts     []ArtifactRef  `json:"artifacts,omitempty"`
	Hash          string         `json:"hash"`        // feeds downstream cache_key (§4.1) + verdict scope_hash (§4.2)
	AcceptedAt    string         `json:"accepted_at"` // RFC3339 UTC
	SourceAttempt string         `json:"source_attempt_id"`
	// Children carries the frozen accepted output of each accepted child, set only
	// on a composite's rollup output. A composite produces no work of its own, so
	// this is how its deliverables travel with the parent: a reader of the root
	// goal gets the tree's results without walking children. Nested composites
	// compose (a child composite's Children is already filled when it accepted).
	// Empty for leaves. Bounded by fanout (max_concurrent) x depth (max_depth).
	Children []AcceptedOutput `json:"children,omitempty"`
}

// AcceptanceEventDetail is the truncated/hash-addressed payload on an
// acceptance event (contract §3.6). The verdict quartet
// (rationale/scope/authority/timestamp) + scope_hash are first-class columns,
// not this blob.
type AcceptanceEventDetail struct {
	Stdout     string        `json:"stdout,omitempty"` // truncated to N KB
	Artifacts  []ArtifactRef `json:"artifacts,omitempty"`
	DurationMs int64         `json:"duration_ms,omitempty"`
	CacheHit   bool          `json:"cache_hit,omitempty"`
	Gaps       []Gap         `json:"gaps,omitempty"`
}

// emptyJSON is the canonical empty-object value for the JSONB columns.
var emptyJSON = json.RawMessage("{}")

// marshalJSON encodes v to compact JSON for a JSONB column. A nil or failed
// marshal degrades to "{}" so a column is never empty/invalid — the write
// boundary validates shape, this just guarantees a parseable column.
func marshalJSON(v any) json.RawMessage {
	if v == nil {
		return emptyJSON
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return emptyJSON
	}
	return b
}

// unmarshalJSON decodes a JSONB column into v. An empty value is treated as the
// empty object so a default '{}' column round-trips to a zero value.
func unmarshalJSON(s json.RawMessage, v any) error {
	if len(s) == 0 {
		s = emptyJSON
	}
	return json.Unmarshal(s, v)
}

// marshalNullJSON freezes v into a non-NULL accepted_output (nullable TEXT-JSON
// column). Acceptance always sets a value, so Valid is always true.
func marshalNullJSON(v any) pgtype.Text {
	return pgtype.Text{String: string(marshalJSON(v)), Valid: true}
}

// unmarshalNullJSON decodes a nullable TEXT-JSON column. SQL NULL (the
// not-yet-accepted state) leaves v at its zero value and reports no error.
func unmarshalNullJSON(ns pgtype.Text, v any) error {
	if !ns.Valid {
		return nil
	}
	return unmarshalJSON(json.RawMessage(ns.String), v)
}

// ContentHash is the canonical content-addressing hash used for
// AcceptedOutput.Hash and any artifact/verdict scope anchor. It hashes the
// JSON-canonical encoding of the parts so two structurally equal outputs hash
// identically. A single constructor keeps the cache key, the accepted-output
// hash, and the verdict scope_hash in agreement.
func ContentHash(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		// Marshal each part separately and NUL-separate so concatenation is
		// unambiguous (a||bc never collides with ab||c).
		b, err := json.Marshal(p)
		if err != nil {
			b = nil
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashOutput computes the content hash of a candidate output's salient fields
// (summary + structured result). Stable under map ordering because
// json.Marshal sorts map keys. Artifacts are hash-addressed separately on the
// evidence; HashWithArtifacts folds them in when an output's identity must
// include its externalized artifacts.
func HashOutput(out AttemptOutput) string {
	return ContentHash(out.Summary, out.Result)
}

// HashWithArtifacts extends an output hash with the sorted artifact hashes, so
// two outputs with identical prose but different artifacts hash distinctly.
func HashWithArtifacts(out AttemptOutput, arts []ArtifactRef) string {
	return ContentHash(out.Summary, out.Result, artifactHashes(arts))
}

// artifactHashes returns the sorted artifact hashes — used inside content
// hashing so artifact order never perturbs the result.
func artifactHashes(arts []ArtifactRef) []string {
	hs := make([]string, 0, len(arts))
	for _, a := range arts {
		hs = append(hs, a.Hash)
	}
	sort.Strings(hs)
	return hs
}
