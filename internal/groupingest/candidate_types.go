package groupingest

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
)

const (
	defaultMaxGroupFactCandidates   = 5
	maxGroupFactTargetsPerOperation = 10
)

const (
	groupScoreEvidenceStrength = "evidence_strength"
	groupScoreSubjectFit       = "subject_fit"
	groupScoreDurability       = "durability"
	groupScoreFutureUtility    = "future_utility"
	groupScoreAtomicity        = "atomicity"
)

var groupScoreFields = []string{
	groupScoreEvidenceStrength,
	groupScoreSubjectFit,
	groupScoreDurability,
	groupScoreFutureUtility,
	groupScoreAtomicity,
}

// GroupCandidateGateSettings keeps Group Reflect tuning injectable while the
// score schema and deterministic tie-break semantics remain fixed in code.
type GroupCandidateGateSettings struct {
	Weights      map[string]float64
	CoreFloor    int
	Threshold    float64
	CandidateCap int
}

type groupCandidateGateJSON struct {
	Weights      map[string]float64 `json:"weights"`
	CoreFloor    *int               `json:"core_floor"`
	Threshold    *float64           `json:"threshold"`
	CandidateCap *int               `json:"candidate_cap"`
}

func defaultGroupCandidateGateSettings() GroupCandidateGateSettings {
	return GroupCandidateGateSettings{
		Weights: map[string]float64{
			groupScoreEvidenceStrength: 0.20,
			groupScoreSubjectFit:       0.20,
			groupScoreDurability:       0.20,
			groupScoreFutureUtility:    0.20,
			groupScoreAtomicity:        0.20,
		},
		CoreFloor:    3,
		Threshold:    0.80,
		CandidateCap: defaultMaxGroupFactCandidates,
	}
}

func (s GroupCandidateGateSettings) withDefaults() GroupCandidateGateSettings {
	defaults := defaultGroupCandidateGateSettings()
	if len(s.Weights) == 0 {
		s.Weights = defaults.Weights
	}
	if s.CoreFloor <= 0 {
		s.CoreFloor = defaults.CoreFloor
	}
	if s.Threshold <= 0 {
		s.Threshold = defaults.Threshold
	}
	if s.CandidateCap <= 0 {
		s.CandidateCap = defaults.CandidateCap
	}
	return s
}

// ParseGroupCandidateGateSettings parses the deployment override while keeping
// the evaluator schema fixed. Blank input selects the evaluated V1 defaults.
func ParseGroupCandidateGateSettings(raw string) (GroupCandidateGateSettings, error) {
	settings := defaultGroupCandidateGateSettings()
	if strings.TrimSpace(raw) == "" {
		return settings, nil
	}

	var override groupCandidateGateJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&override); err != nil {
		return GroupCandidateGateSettings{}, fmt.Errorf("decode Group Reflect candidate gate: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return GroupCandidateGateSettings{}, fmt.Errorf("decode Group Reflect candidate gate: multiple JSON values")
		}
		return GroupCandidateGateSettings{}, fmt.Errorf("decode Group Reflect candidate gate: %w", err)
	}

	if override.Weights != nil {
		if len(override.Weights) != len(groupScoreFields) {
			return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate weights must contain exactly %d score fields", len(groupScoreFields))
		}
		totalWeight := 0.0
		for _, field := range groupScoreFields {
			weight, ok := override.Weights[field]
			if !ok {
				return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate weight %q is required", field)
			}
			if weight < 0 {
				return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate weight %q must be non-negative", field)
			}
			totalWeight += weight
		}
		if totalWeight <= 0 {
			return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate weights must have a positive total")
		}
		settings.Weights = override.Weights
	}
	if override.CoreFloor != nil {
		if *override.CoreFloor < 1 || *override.CoreFloor > 4 {
			return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate core_floor must be between 1 and 4")
		}
		settings.CoreFloor = *override.CoreFloor
	}
	if override.Threshold != nil {
		if *override.Threshold <= 0 || *override.Threshold > 1 {
			return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate threshold must be greater than 0 and at most 1")
		}
		settings.Threshold = *override.Threshold
	}
	if override.CandidateCap != nil {
		if *override.CandidateCap < 1 || *override.CandidateCap > defaultMaxGroupFactCandidates {
			return GroupCandidateGateSettings{}, fmt.Errorf("group Reflect candidate gate candidate_cap must be between 1 and %d", defaultMaxGroupFactCandidates)
		}
		settings.CandidateCap = *override.CandidateCap
	}
	return settings, nil
}

type GroupFactEvidence struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type GroupFactCandidate struct {
	Ref            reflectpkg.CandidateRef `json:"candidate_ref,omitempty"`
	Subject        memory.GroupFactSubject `json:"subject"`
	SubjectRef     string                  `json:"subject_ref,omitempty"`
	Content        string                  `json:"content"`
	Evidence       []GroupFactEvidence     `json:"evidence"`
	ExpectedEffect string                  `json:"expected_effect"`
}

type GroupFactEvaluation struct {
	Ref       reflectpkg.CandidateRef `json:"candidate_ref"`
	Scores    map[string]int          `json:"scores"`
	Rationale string                  `json:"rationale"`
}

type GroupCandidateReviewResult struct {
	Generated []GroupFactCandidate
	Accepted  []GroupFactCandidate
	Gate      reflectpkg.CandidateGateResult
}

func validateGeneratedGroupCandidates(candidates []GroupFactCandidate, unit GroupReviewUnit, candidateCap int) error {
	if len(candidates) > candidateCap {
		return fmt.Errorf("at most %d group fact candidates are allowed", candidateCap)
	}
	for i, candidate := range candidates {
		if err := validateGeneratedGroupCandidate(candidate, unit); err != nil {
			return fmt.Errorf("candidate %d: %w", i, err)
		}
	}
	return nil
}

func validateGeneratedGroupCandidate(candidate GroupFactCandidate, unit GroupReviewUnit) error {
	if strings.TrimSpace(candidate.Content) == "" {
		return fmt.Errorf("content is required")
	}
	for ref := range unit.Subjects {
		if strings.Contains(candidate.Content, ref) {
			return fmt.Errorf("content cannot contain temporary subject_ref %q", ref)
		}
	}
	if strings.TrimSpace(candidate.ExpectedEffect) == "" {
		return fmt.Errorf("expected_effect is required")
	}
	if len(candidate.Evidence) == 0 {
		return fmt.Errorf("at least one evidence item is required")
	}
	for i, evidence := range candidate.Evidence {
		if strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Reason) == "" {
			return fmt.Errorf("evidence %d requires source and reason", i)
		}
	}
	switch candidate.Subject {
	case memory.GroupFactSubjectGroup:
		if candidate.SubjectRef != "" {
			return fmt.Errorf("group subject cannot have subject_ref")
		}
	case memory.GroupFactSubjectHuman, memory.GroupFactSubjectAgent:
		entry, ok := unit.Subjects[candidate.SubjectRef]
		if !ok {
			return fmt.Errorf("subject_ref %q is not in this review", candidate.SubjectRef)
		}
		if entry.Subject != candidate.Subject {
			return fmt.Errorf("subject_ref %q has subject %q, not %q", candidate.SubjectRef, entry.Subject, candidate.Subject)
		}
	default:
		return fmt.Errorf("unsupported subject %q", candidate.Subject)
	}
	return nil
}

func validateGroupEvaluations(evaluations []GroupFactEvaluation, expected []reflectpkg.CandidateRef) error {
	expectedSet := make(map[reflectpkg.CandidateRef]struct{}, len(expected))
	for _, ref := range expected {
		expectedSet[ref] = struct{}{}
	}
	seen := make(map[reflectpkg.CandidateRef]struct{}, len(evaluations))
	for i, evaluation := range evaluations {
		if _, ok := expectedSet[evaluation.Ref]; !ok {
			return fmt.Errorf("evaluation %d has unknown candidate_ref %q", i, evaluation.Ref)
		}
		if _, ok := seen[evaluation.Ref]; ok {
			return fmt.Errorf("evaluation %d duplicates candidate_ref %q", i, evaluation.Ref)
		}
		seen[evaluation.Ref] = struct{}{}
		if strings.TrimSpace(evaluation.Rationale) == "" {
			return fmt.Errorf("evaluation %d requires rationale", i)
		}
		if len(evaluation.Scores) != len(groupScoreFields) {
			return fmt.Errorf("evaluation %d score schema mismatch", i)
		}
		for _, field := range groupScoreFields {
			score, ok := evaluation.Scores[field]
			if !ok || score < 0 || score > 4 {
				return fmt.Errorf("evaluation %d has invalid score %q", i, field)
			}
		}
	}
	for _, ref := range expected {
		if _, ok := seen[ref]; !ok {
			return fmt.Errorf("missing evaluation for candidate_ref %q", ref)
		}
	}
	return nil
}
