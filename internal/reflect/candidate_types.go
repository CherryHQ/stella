package reflect

import "fmt"

// CandidateRef is a stable in-review identifier assigned after generation.
// It is intentionally sequential in #532; hash-based refs are deferred.
type CandidateRef string

type RejectReason string

const (
	rejectSecretDetected        RejectReason = "secret_detected"
	rejectScoreFloorFailed      RejectReason = "score_floor_failed"
	rejectOverallBelowThreshold RejectReason = "overall_below_threshold"
	rejectSchemaMissingField    RejectReason = "schema_missing_field"
	rejectSchemaExtraField      RejectReason = "schema_extra_field"
	rejectCapDropped            RejectReason = "cap_dropped"
	rejectScopeNotEligible      RejectReason = "scope_not_eligible"
)

type CandidateGateInput struct {
	Ref     CandidateRef
	Content string
	Scores  map[string]int
}

type CandidateGateConfig struct {
	Weights        map[string]float64
	CoreFields     []string
	CoreFloor      int
	Threshold      float64
	Cap            int
	TieBreakFields []string
}

type CandidateGateDecision struct {
	Ref               CandidateRef
	NormalizedOverall float64
	Scores            map[string]int
	Reason            RejectReason
}

type CandidateGateResult struct {
	Accepted []CandidateGateDecision
	Rejected []CandidateGateDecision
}

// factCandidateDecision keeps every accepted Fact candidate tied to the exact
// evaluator and deterministic gate result used by later reconciliation.
type factCandidateDecision struct {
	Candidate  factCandidate
	Evaluation factEvaluation
	Gate       CandidateGateDecision
}

// skillCandidateDecision is the Skill-line equivalent of
// factCandidateDecision. Rejected candidates intentionally do not enter this
// success-only handoff.
type skillCandidateDecision struct {
	Candidate  skillCandidate
	Evaluation skillEvaluation
	Gate       CandidateGateDecision
}

func candidateRef(prefix string, index int) CandidateRef {
	return CandidateRef(fmt.Sprintf("%s-%04d", prefix, index+1))
}

func assignCandidateRefs(prefix string, count int) []CandidateRef {
	refs := make([]CandidateRef, 0, count)
	for i := range count {
		refs = append(refs, candidateRef(prefix, i))
	}
	return refs
}

// AssignCandidateRefs returns deterministic host-owned refs for one review.
func AssignCandidateRefs(prefix string, count int) []CandidateRef {
	return assignCandidateRefs(prefix, count)
}
