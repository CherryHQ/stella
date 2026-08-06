package groupingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"

	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
)

type groupGenerationPayload struct {
	Candidates        []GroupFactCandidate `json:"candidates"`
	NoCandidateReason string               `json:"no_candidate_reason,omitempty"`
}

type groupEvaluationPayload struct {
	Evaluations []GroupFactEvaluation `json:"evaluations"`
}

type CandidateReviewer struct {
	Stream      providers.StreamFunc
	Model       ai.Model
	Options     ai.CompleteOptions
	Gates       GroupCandidateGateSettings
	OnGenerated func(int)
}

func (r CandidateReviewer) Run(ctx context.Context, unit GroupReviewUnit) (GroupCandidateReviewResult, error) {
	settings := r.Gates.withDefaults()
	candidates, err := r.generate(ctx, unit, settings.CandidateCap)
	if err != nil {
		return GroupCandidateReviewResult{}, err
	}
	result := GroupCandidateReviewResult{Generated: candidates}
	if r.OnGenerated != nil {
		r.OnGenerated(len(candidates))
	}
	if len(candidates) == 0 {
		return result, nil
	}

	evaluations, err := r.evaluate(ctx, unit, candidates)
	if err != nil {
		return GroupCandidateReviewResult{}, err
	}
	result.Gate = gateGroupFactCandidates(candidates, evaluations, settings)
	result.Accepted = acceptedGroupFactCandidates(candidates, result.Gate.Accepted)
	return result, nil
}

func (r CandidateReviewer) generate(
	ctx context.Context,
	unit GroupReviewUnit,
	candidateCap int,
) ([]GroupFactCandidate, error) {
	var candidates []GroupFactCandidate
	runner := reflectpkg.StructuredCaptureRunner{Stream: r.Stream, Model: r.Model, Options: r.Options}
	_, err := runner.Run(ctx, renderGroupFactGenerationPrompt(candidateCap), unit.Text, reflectpkg.StructuredCaptureProtocol{
		AllowedTools:  reflectpkg.AllowedCaptureTools(toolSubmitGroupFactGeneration),
		SubmitName:    toolSubmitGroupFactGeneration,
		RepairRetries: true,
		RepairInstructions: []string{
			"Top-level arguments may only contain candidates and no_candidate_reason.",
			"If candidates is empty, provide a non-empty no_candidate_reason.",
			"If candidates is non-empty, omit no_candidate_reason.",
			fmt.Sprintf("Return no more than %d candidates.", candidateCap),
		},
		PayloadsValidator: func(calls []ai.ToolCall) error {
			payload, decodeErr := decodeGroupGeneration(calls)
			if decodeErr != nil {
				return decodeErr
			}
			if validateErr := validateGenerationBatch(payload); validateErr != nil {
				return validateErr
			}
			if validateErr := validateGeneratedGroupCandidates(payload.Candidates, unit, candidateCap); validateErr != nil {
				return validateErr
			}
			candidates = payload.Candidates
			return nil
		},
	}, groupFactGenerationTools(candidateCap))
	if err != nil {
		return nil, fmt.Errorf("generate group fact candidates: %w", err)
	}
	refs := reflectpkg.AssignCandidateRefs("group-fact", len(candidates))
	for i := range candidates {
		candidates[i].Ref = refs[i]
	}
	return candidates, nil
}

func (r CandidateReviewer) evaluate(
	ctx context.Context,
	unit GroupReviewUnit,
	candidates []GroupFactCandidate,
) ([]GroupFactEvaluation, error) {
	expected := make([]reflectpkg.CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		expected = append(expected, candidate.Ref)
	}
	var evaluations []GroupFactEvaluation
	runner := reflectpkg.StructuredCaptureRunner{Stream: r.Stream, Model: r.Model, Options: r.Options}
	_, err := runner.Run(
		ctx,
		groupFactEvaluationPrompt,
		renderGroupEvaluationInput(unit.Text, candidates),
		reflectpkg.StructuredCaptureProtocol{
			AllowedTools:  reflectpkg.AllowedCaptureTools(toolSubmitGroupFactEvaluations),
			SubmitName:    toolSubmitGroupFactEvaluations,
			RepairRetries: true,
			RepairInstructions: []string{
				"Submit exactly one evaluation for every host-assigned candidate_ref.",
				"Scores must contain exactly the five rubric fields with integer values from 0 to 4.",
			},
			PayloadsValidator: func(calls []ai.ToolCall) error {
				payload, decodeErr := decodeGroupEvaluations(calls)
				if decodeErr != nil {
					return decodeErr
				}
				if validateErr := validateGroupEvaluations(payload.Evaluations, expected); validateErr != nil {
					return validateErr
				}
				evaluations = payload.Evaluations
				return nil
			},
		},
		groupFactEvaluationTools(),
	)
	if err != nil {
		return nil, fmt.Errorf("evaluate group fact candidates: %w", err)
	}
	return evaluations, nil
}

func decodeGroupGeneration(calls []ai.ToolCall) (groupGenerationPayload, error) {
	for _, call := range calls {
		if call.Name == toolSubmitGroupFactGeneration {
			return reflectpkg.DecodeStructuredCapturePayload[groupGenerationPayload](call)
		}
	}
	return groupGenerationPayload{}, fmt.Errorf("missing %s", toolSubmitGroupFactGeneration)
}

func decodeGroupEvaluations(calls []ai.ToolCall) (groupEvaluationPayload, error) {
	for _, call := range calls {
		if call.Name == toolSubmitGroupFactEvaluations {
			return reflectpkg.DecodeStructuredCapturePayload[groupEvaluationPayload](call)
		}
	}
	return groupEvaluationPayload{}, fmt.Errorf("missing %s", toolSubmitGroupFactEvaluations)
}

func validateGenerationBatch(payload groupGenerationPayload) error {
	reason := strings.TrimSpace(payload.NoCandidateReason)
	if len(payload.Candidates) == 0 {
		if reason == "" {
			return fmt.Errorf("empty candidate batch requires no_candidate_reason")
		}
		return nil
	}
	if reason != "" {
		return fmt.Errorf("non-empty candidate batch must omit no_candidate_reason")
	}
	return nil
}

func gateGroupFactCandidates(
	candidates []GroupFactCandidate,
	evaluations []GroupFactEvaluation,
	settings GroupCandidateGateSettings,
) reflectpkg.CandidateGateResult {
	settings = settings.withDefaults()
	evaluationByRef := make(map[reflectpkg.CandidateRef]GroupFactEvaluation, len(evaluations))
	for _, evaluation := range evaluations {
		evaluationByRef[evaluation.Ref] = evaluation
	}
	inputs := make([]reflectpkg.CandidateGateInput, 0, len(candidates))
	for _, candidate := range candidates {
		candidateJSON, _ := json.Marshal(candidate)
		inputs = append(inputs, reflectpkg.CandidateGateInput{
			Ref:     candidate.Ref,
			Content: string(candidateJSON),
			Scores:  evaluationByRef[candidate.Ref].Scores,
		})
	}
	return reflectpkg.GateCandidates(inputs, reflectpkg.CandidateGateConfig{
		Weights:        settings.Weights,
		CoreFields:     append([]string(nil), groupScoreFields...),
		CoreFloor:      settings.CoreFloor,
		Threshold:      settings.Threshold,
		Cap:            settings.CandidateCap,
		TieBreakFields: []string{groupScoreEvidenceStrength, groupScoreFutureUtility, groupScoreDurability},
	})
}

func acceptedGroupFactCandidates(
	candidates []GroupFactCandidate,
	decisions []reflectpkg.CandidateGateDecision,
) []GroupFactCandidate {
	byRef := make(map[reflectpkg.CandidateRef]GroupFactCandidate, len(candidates))
	for _, candidate := range candidates {
		byRef[candidate.Ref] = candidate
	}
	accepted := make([]GroupFactCandidate, 0, len(decisions))
	for _, decision := range decisions {
		accepted = append(accepted, byRef[decision.Ref])
	}
	return accepted
}

func renderGroupEvaluationInput(reviewText string, candidates []GroupFactCandidate) string {
	data, _ := json.Marshal(candidates)
	return reviewText + "\n<candidates_json>\n" + string(data) + "\n</candidates_json>\n"
}
