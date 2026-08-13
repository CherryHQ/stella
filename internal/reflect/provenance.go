package reflect

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
)

const (
	reflectProvenanceSchemaVersion = 1
	maxReflectProvenanceBytes      = 64 << 10
)

var (
	errReflectProvenanceTooLarge       = errors.New("reflect provenance exceeds size limit")
	errReflectProvenanceSecretDetected = errors.New("reflect provenance contains secret-like content")
)

type reflectProvenanceContext struct {
	RunID     string
	SessionID string
	ModelID   string
	Boundary  reflectProvenanceReviewBoundary
}

type factProvenanceInput struct {
	Context   reflectProvenanceContext
	Decisions []factCandidateDecision
}

type skillProvenanceInput struct {
	Context   reflectProvenanceContext
	Decisions []skillCandidateDecision
}

type reflectProvenanceReviewBoundary struct {
	From reflectProvenanceReviewPoint `json:"from"`
	To   reflectProvenanceReviewPoint `json:"to"`
}

type reflectProvenanceReviewPoint struct {
	Seq int64  `json:"seq,omitempty"`
	At  string `json:"at,omitempty"`
}

type reflectProvenanceHeader struct {
	SchemaVersion  int                             `json:"schema_version"`
	RunID          string                          `json:"run_id"`
	OperationRef   string                          `json:"operation_ref"`
	Line           reflectLine                     `json:"line"`
	SessionID      string                          `json:"session_id"`
	ReviewBoundary reflectProvenanceReviewBoundary `json:"review_boundary"`
	Model          string                          `json:"model"`
}

type reflectProvenanceEvaluation struct {
	CandidateRef      CandidateRef   `json:"candidate_ref"`
	Scores            map[string]int `json:"scores"`
	Rationale         string         `json:"rationale"`
	NormalizedOverall float64        `json:"normalized_overall"`
}

type factOperationProvenance struct {
	reflectProvenanceHeader
	Candidates     []factCandidate               `json:"candidates"`
	Evaluations    []reflectProvenanceEvaluation `json:"evaluations"`
	RelatedRecords []factProvenanceRelatedRecord `json:"related_records"`
	Reconciliation factProvenanceReconciliation  `json:"reconciliation"`
}

type skillOperationProvenance struct {
	reflectProvenanceHeader
	Candidates     []skillCandidate               `json:"candidates"`
	Evaluations    []reflectProvenanceEvaluation  `json:"evaluations"`
	RelatedRecords []skillProvenanceRelatedRecord `json:"related_records"`
	Reconciliation skillProvenanceReconciliation  `json:"reconciliation"`
}

type factProvenanceRelatedRecord struct {
	CandidateRef CandidateRef          `json:"candidate_ref"`
	FactID       string                `json:"fact_id"`
	Version      int64                 `json:"version"`
	Relation     knowledgeRelationKind `json:"relation"`
	Reason       string                `json:"reason,omitempty"`
}

type skillProvenanceRelatedRecord struct {
	CandidateRef  CandidateRef      `json:"candidate_ref"`
	SkillID       string            `json:"skill_id"`
	Version       int64             `json:"version"`
	ContentDigest string            `json:"content_digest"`
	Relation      skillRelationKind `json:"relation"`
	Reason        string            `json:"reason,omitempty"`
}

type factProvenanceReconciliation struct {
	Operation            string         `json:"operation"`
	CandidateRefs        []CandidateRef `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs []CandidateRef `json:"covered_candidate_refs,omitempty"`
	TargetFactIDs        []string       `json:"target_fact_ids,omitempty"`
	Rationale            string         `json:"rationale"`
}

type skillProvenanceReconciliation struct {
	Operation            string         `json:"operation"`
	CandidateRefs        []CandidateRef `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs []CandidateRef `json:"covered_candidate_refs,omitempty"`
	TargetSkillID        string         `json:"target_skill_id,omitempty"`
	ExpectedSkillVersion int64          `json:"expected_skill_version,omitempty"`
	ExpectedSkillDigest  string         `json:"expected_skill_digest,omitempty"`
	Name                 string         `json:"name,omitempty"`
	Description          string         `json:"description,omitempty"`
	MainFileSHA256       string         `json:"main_file_sha256"`
	MainFileBytes        int            `json:"main_file_bytes"`
	Rationale            string         `json:"rationale"`
}

type reflectProvenanceMetadata[T any] struct {
	ReflectProvenance T `json:"reflect_provenance"`
}

func newReflectProvenanceContext(sessionID string, modelID string, unit ReviewUnit) (reflectProvenanceContext, error) {
	if sessionID == "" {
		return reflectProvenanceContext{}, fmt.Errorf("reflect provenance: session id is required")
	}
	if modelID == "" {
		return reflectProvenanceContext{}, fmt.Errorf("reflect provenance: model id is required")
	}
	if unit.LastIncludedSeq == 0 && unit.LastIncludedAt.IsZero() {
		return reflectProvenanceContext{}, fmt.Errorf("reflect provenance: review end boundary is required")
	}
	return reflectProvenanceContext{
		RunID:     uuid.Must(uuid.NewV7()).String(),
		SessionID: sessionID,
		ModelID:   modelID,
		Boundary: reflectProvenanceReviewBoundary{
			From: reflectProvenanceReviewPoint{
				Seq: unit.ReviewFromSeq,
				At:  formatReflectProvenanceTime(unit.ReviewFromAt),
			},
			To: reflectProvenanceReviewPoint{
				Seq: unit.LastIncludedSeq,
				At:  formatReflectProvenanceTime(unit.LastIncludedAt),
			},
		},
	}, nil
}

func formatReflectProvenanceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func buildFactOperationProvenance(
	input factProvenanceInput,
	operationRef string,
	operation string,
	candidateRefs []CandidateRef,
	coveredCandidateRefs []CandidateRef,
	targetFactIDs []string,
	knowledge *knowledgeRelatedBundle,
	rationale string,
) (json.RawMessage, error) {
	header, err := buildReflectProvenanceHeader(input.Context, reflectLineFact, operationRef)
	if err != nil {
		return nil, err
	}
	candidates, evaluations, err := selectFactProvenanceDecisions(input.Decisions, candidateRefs, coveredCandidateRefs)
	if err != nil {
		return nil, err
	}
	related := make([]factProvenanceRelatedRecord, 0)
	if knowledge != nil {
		related, err = projectFactProvenanceRelatedRecords(*knowledge, candidateRefsForOperation(candidateRefs, coveredCandidateRefs))
		if err != nil {
			return nil, err
		}
	}
	return marshalReflectProvenance(factOperationProvenance{
		reflectProvenanceHeader: header,
		Candidates:              candidates,
		Evaluations:             evaluations,
		RelatedRecords:          related,
		Reconciliation: factProvenanceReconciliation{
			Operation:            operation,
			CandidateRefs:        append([]CandidateRef(nil), candidateRefs...),
			CoveredCandidateRefs: append([]CandidateRef(nil), coveredCandidateRefs...),
			TargetFactIDs:        append([]string(nil), targetFactIDs...),
			Rationale:            rationale,
		},
	})
}

func buildSkillPlanProvenance(input skillProvenanceInput, bundle skillRelatedBundle, plan skillReconciliationPlan) (map[int]json.RawMessage, error) {
	metadata := make(map[int]json.RawMessage, len(plan.Operations))
	for index, operation := range plan.Operations {
		if operation.Operation == skillOperationNoop {
			continue
		}
		value, err := buildSkillOperationProvenance(input, bundle, operation, fmt.Sprintf("skill-%04d", index+1))
		if err != nil {
			return nil, err
		}
		metadata[index] = value
	}
	return metadata, nil
}

func buildSkillOperationProvenance(input skillProvenanceInput, bundle skillRelatedBundle, operation skillWriteOperation, operationRef string) (json.RawMessage, error) {
	header, err := buildReflectProvenanceHeader(input.Context, reflectLineSkill, operationRef)
	if err != nil {
		return nil, err
	}
	candidates, evaluations, err := selectSkillProvenanceDecisions(input.Decisions, operation.CandidateRefs, operation.CoveredCandidateRefs)
	if err != nil {
		return nil, err
	}
	related, err := projectSkillProvenanceRelatedRecords(bundle, candidateRefsForOperation(operation.CandidateRefs, operation.CoveredCandidateRefs))
	if err != nil {
		return nil, err
	}
	contentHash := sha256.Sum256([]byte(operation.MainFileContent))
	return marshalReflectProvenance(skillOperationProvenance{
		reflectProvenanceHeader: header,
		Candidates:              candidates,
		Evaluations:             evaluations,
		RelatedRecords:          related,
		Reconciliation: skillProvenanceReconciliation{
			Operation:            string(operation.Operation),
			CandidateRefs:        append([]CandidateRef(nil), operation.CandidateRefs...),
			CoveredCandidateRefs: append([]CandidateRef(nil), operation.CoveredCandidateRefs...),
			TargetSkillID:        operation.TargetSkillID,
			ExpectedSkillVersion: operation.ExpectedSkillVersion,
			ExpectedSkillDigest:  operation.ExpectedSkillDigest,
			Name:                 operation.Name,
			Description:          operation.Description,
			MainFileSHA256:       fmt.Sprintf("%x", contentHash),
			MainFileBytes:        len([]byte(operation.MainFileContent)),
			Rationale:            operation.Rationale,
		},
	})
}

func buildReflectProvenanceHeader(input reflectProvenanceContext, line reflectLine, operationRef string) (reflectProvenanceHeader, error) {
	if input.RunID == "" || input.SessionID == "" || input.ModelID == "" {
		return reflectProvenanceHeader{}, fmt.Errorf("reflect provenance: run, session, and model ids are required")
	}
	if operationRef == "" {
		return reflectProvenanceHeader{}, fmt.Errorf("reflect provenance: operation ref is required")
	}
	return reflectProvenanceHeader{
		SchemaVersion:  reflectProvenanceSchemaVersion,
		RunID:          input.RunID,
		OperationRef:   operationRef,
		Line:           line,
		SessionID:      input.SessionID,
		ReviewBoundary: input.Boundary,
		Model:          input.ModelID,
	}, nil
}

func selectFactProvenanceDecisions(decisions []factCandidateDecision, direct []CandidateRef, covered []CandidateRef) ([]factCandidate, []reflectProvenanceEvaluation, error) {
	byRef := make(map[CandidateRef]factCandidateDecision, len(decisions))
	for _, decision := range decisions {
		ref := decision.Candidate.Ref
		if ref == "" || decision.Evaluation.Ref != ref || decision.Gate.Ref != ref {
			return nil, nil, fmt.Errorf("reflect provenance: inconsistent fact decision refs")
		}
		if _, exists := byRef[ref]; exists {
			return nil, nil, fmt.Errorf("reflect provenance: duplicate fact decision %q", ref)
		}
		byRef[ref] = decision
	}
	refs := candidateRefsForOperation(direct, covered)
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("reflect provenance: fact write operation has no candidate refs")
	}
	candidates := make([]factCandidate, 0, len(refs))
	evaluations := make([]reflectProvenanceEvaluation, 0, len(refs))
	for _, ref := range refs {
		decision, ok := byRef[ref]
		if !ok {
			return nil, nil, fmt.Errorf("reflect provenance: fact candidate %q is missing from accepted decisions", ref)
		}
		candidates = append(candidates, decision.Candidate)
		evaluations = append(evaluations, projectReflectProvenanceEvaluation(decision.Evaluation.Ref, decision.Evaluation.Scores, decision.Evaluation.Rationale, decision.Gate))
	}
	return candidates, evaluations, nil
}

func selectSkillProvenanceDecisions(decisions []skillCandidateDecision, direct []CandidateRef, covered []CandidateRef) ([]skillCandidate, []reflectProvenanceEvaluation, error) {
	byRef := make(map[CandidateRef]skillCandidateDecision, len(decisions))
	for _, decision := range decisions {
		ref := decision.Candidate.Ref
		if ref == "" || decision.Evaluation.Ref != ref || decision.Gate.Ref != ref {
			return nil, nil, fmt.Errorf("reflect provenance: inconsistent skill decision refs")
		}
		if _, exists := byRef[ref]; exists {
			return nil, nil, fmt.Errorf("reflect provenance: duplicate skill decision %q", ref)
		}
		byRef[ref] = decision
	}
	refs := candidateRefsForOperation(direct, covered)
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("reflect provenance: skill write operation has no candidate refs")
	}
	candidates := make([]skillCandidate, 0, len(refs))
	evaluations := make([]reflectProvenanceEvaluation, 0, len(refs))
	for _, ref := range refs {
		decision, ok := byRef[ref]
		if !ok {
			return nil, nil, fmt.Errorf("reflect provenance: skill candidate %q is missing from accepted decisions", ref)
		}
		candidates = append(candidates, decision.Candidate)
		evaluations = append(evaluations, projectReflectProvenanceEvaluation(decision.Evaluation.Ref, decision.Evaluation.Scores, decision.Evaluation.Rationale, decision.Gate))
	}
	return candidates, evaluations, nil
}

func projectReflectProvenanceEvaluation(ref CandidateRef, scores map[string]int, rationale string, gate CandidateGateDecision) reflectProvenanceEvaluation {
	clonedScores := make(map[string]int, len(scores))
	maps.Copy(clonedScores, scores)
	return reflectProvenanceEvaluation{
		CandidateRef:      ref,
		Scores:            clonedScores,
		Rationale:         rationale,
		NormalizedOverall: gate.NormalizedOverall,
	}
}

func projectFactProvenanceRelatedRecords(bundle knowledgeRelatedBundle, selectedRefs []CandidateRef) ([]factProvenanceRelatedRecord, error) {
	selected := candidateRefSet(selectedRefs)
	records := make(map[string]memory.Fact, len(bundle.RelatedRecords))
	for _, record := range bundle.RelatedRecords {
		records[record.ID] = record
	}
	out := make([]factProvenanceRelatedRecord, 0)
	for _, selection := range bundle.RelationHints {
		if _, ok := selected[selection.CandidateRef]; !ok {
			continue
		}
		for _, hint := range selection.Related {
			record, ok := records[hint.FactID]
			if !ok {
				return nil, fmt.Errorf("reflect provenance: related fact %q is missing from bundle", hint.FactID)
			}
			out = append(out, factProvenanceRelatedRecord{
				CandidateRef: selection.CandidateRef,
				FactID:       hint.FactID,
				Version:      record.Version,
				Relation:     hint.Relation,
				Reason:       selection.Reason,
			})
		}
	}
	return out, nil
}

func projectSkillProvenanceRelatedRecords(bundle skillRelatedBundle, selectedRefs []CandidateRef) ([]skillProvenanceRelatedRecord, error) {
	selected := candidateRefSet(selectedRefs)
	records := make(map[string]skillRelatedRecord, len(bundle.RelatedRecords))
	for _, record := range bundle.RelatedRecords {
		records[record.Skill.ID] = record
	}
	out := make([]skillProvenanceRelatedRecord, 0)
	for _, selection := range bundle.RelationHints {
		if _, ok := selected[selection.CandidateRef]; !ok {
			continue
		}
		for _, hint := range selection.Related {
			record, ok := records[hint.SkillID]
			if !ok {
				return nil, fmt.Errorf("reflect provenance: related skill %q is missing from bundle", hint.SkillID)
			}
			out = append(out, skillProvenanceRelatedRecord{
				CandidateRef:  selection.CandidateRef,
				SkillID:       hint.SkillID,
				Version:       record.Skill.Version,
				ContentDigest: record.Skill.ContentDigest,
				Relation:      hint.Relation,
				Reason:        selection.Reason,
			})
		}
	}
	return out, nil
}

func candidateRefsForOperation(direct []CandidateRef, covered []CandidateRef) []CandidateRef {
	return uniqueCandidateRefs(append(append([]CandidateRef(nil), direct...), covered...))
}

func candidateRefSet(refs []CandidateRef) map[CandidateRef]struct{} {
	out := make(map[CandidateRef]struct{}, len(refs))
	for _, ref := range refs {
		out[ref] = struct{}{}
	}
	return out
}

func marshalReflectProvenance[T any](payload T) (json.RawMessage, error) {
	metadata := reflectProvenanceMetadata[T]{ReflectProvenance: payload}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal reflect provenance: %w", err)
	}
	if len(encoded) > maxReflectProvenanceBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", errReflectProvenanceTooLarge, len(encoded), maxReflectProvenanceBytes)
	}
	var persisted any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		return nil, fmt.Errorf("inspect reflect provenance: %w", err)
	}
	if containsReflectProvenanceSecret(persisted) {
		return nil, errReflectProvenanceSecretDetected
	}
	return json.RawMessage(encoded), nil
}

// containsReflectProvenanceSecret scans the exact JSON payload passed to the
// persistence boundary. It intentionally does not depend on candidate gates:
// evaluator and reconciliation text is produced after those gates run.
func containsReflectProvenanceSecret(value any) bool {
	switch typed := value.(type) {
	case string:
		return containsSecretLikeContent(typed)
	case []any:
		if slices.ContainsFunc(typed, containsReflectProvenanceSecret) {
			return true
		}
	case map[string]any:
		for key, item := range typed {
			if containsSecretLikeContent(key) || containsReflectProvenanceSecret(item) {
				return true
			}
		}
	}
	return false
}
