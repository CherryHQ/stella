package deliverable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// AttemptInput is the frozen-at-mint context an attempt executes against
// (contract §3.3). Marshaled to the attempt's input_context column. Frozen
// means an in-flight edit to intent/contract never mutates a running attempt.
type AttemptInput struct {
	Intent          string             `json:"intent"`
	UpstreamOutputs []AcceptedOutput   `json:"upstream_outputs"`           // ONLY accepted upstream outputs
	PriorGaps       *Evaluation        `json:"prior_gaps,omitempty"`       // Evaluation[i-1].gaps; nil on attempt 1
	Contract        AcceptanceContract `json:"contract"`                   // the bar the attempt must clear
	ResolvedVerdict string             `json:"resolved_verdict,omitempty"` // a human answer that unblocked needs_verdict
	AttemptNo       int                `json:"attempt_no"`
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
// deliverable submitting an empty Summary is a retryable protocol miss.
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
	DeliverableID string         `json:"deliverable_id"`
	Summary       string         `json:"summary"`
	Result        map[string]any `json:"result,omitempty"`
	Artifacts     []ArtifactRef  `json:"artifacts,omitempty"`
	Hash          string         `json:"hash"`        // feeds downstream cache_key (§4.1) + verdict scope_hash (§4.2)
	AcceptedAt    string         `json:"accepted_at"` // RFC3339 UTC
	SourceAttempt string         `json:"source_attempt_id"`
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

// emptyJSON is the canonical empty-object value for the JSON TEXT columns.
const emptyJSON = "{}"

// marshalJSON encodes v to a compact JSON string for a TEXT column. A nil or
// failed marshal degrades to "{}" so a column is never empty/invalid — the
// write boundary validates shape, this just guarantees a parseable column.
func marshalJSON(v any) string {
	if v == nil {
		return emptyJSON
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return emptyJSON
	}
	return string(b)
}

// unmarshalJSON decodes a TEXT column into v. An empty string is treated as the
// empty object so a default '{}' column round-trips to a zero value.
func unmarshalJSON(s string, v any) error {
	if s == "" {
		s = emptyJSON
	}
	return json.Unmarshal([]byte(s), v)
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
