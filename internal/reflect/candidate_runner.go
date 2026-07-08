package reflect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	toolSubmitFactGeneration  = "submit_fact_generation"
	toolSubmitFactEvaluations = "submit_fact_evaluations"

	toolSubmitSkillGeneration  = "submit_skill_generation"
	toolSubmitSkillEvaluations = "submit_skill_evaluations"
)

// Capture payloads batch each phase into one tool call. The single submit call
// is also the completion signal, which avoids relying on a second finish tool.
type factGenerationCapturePayload struct {
	Candidates        []factCandidate `json:"candidates"`
	NoCandidateReason string          `json:"no_candidate_reason,omitempty"`
}

type factEvaluationCapturePayload struct {
	Evaluations []factEvaluation `json:"evaluations"`
}

type skillGenerationCapturePayload struct {
	Candidates        []skillCandidate `json:"candidates"`
	NoCandidateReason string           `json:"no_candidate_reason,omitempty"`
}

type skillEvaluationCapturePayload struct {
	Evaluations []skillEvaluation `json:"evaluations"`
}

// candidateLineReviewer runs the #532 in-memory candidate capture protocol. The
// capture tools are not executed; their tool-call arguments are the structured
// output collected from the provider stream.
type candidateLineReviewer struct {
	Stream  providers.StreamFunc
	Model   ai.Model
	Options ai.CompleteOptions
	Gates   CandidateGateSettings
}

func (r candidateLineReviewer) runFactLine(ctx context.Context, unit ReviewUnit) ([]factCandidate, error) {
	candidates, err := r.generateFactCandidates(ctx, unit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	evaluations, err := r.evaluateFactCandidates(ctx, unit, candidates)
	if err != nil {
		return nil, err
	}
	gated := gateFactCandidatesWithSettings(candidates, evaluations, factGateOptions{PrivateOneToOne: unit.PrivateOneToOne}, r.Gates)
	return acceptedFactCandidates(candidates, gated.Accepted), nil
}

func (r candidateLineReviewer) runSkillLine(ctx context.Context, unit ReviewUnit) ([]skillCandidate, error) {
	candidates, err := r.generateSkillCandidates(ctx, unit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	evaluations, err := r.evaluateSkillCandidates(ctx, unit, candidates)
	if err != nil {
		return nil, err
	}
	gated := gateSkillCandidatesWithSettings(candidates, evaluations, r.Gates)
	return acceptedSkillCandidates(candidates, gated.Accepted), nil
}

func (r candidateLineReviewer) generateFactCandidates(ctx context.Context, unit ReviewUnit) ([]factCandidate, error) {
	var candidates []factCandidate
	_, err := r.capture(ctx, factCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitFactGeneration),
		SubmitName:   toolSubmitFactGeneration,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			candidates, err = decodeFactGenerationCall(calls)
			return err
		},
	}, factGenerationTools())
	if err != nil {
		return nil, fmt.Errorf("generate fact candidates: %w", err)
	}
	for i := range candidates {
		candidates[i].Ref = candidateRef("fact", i)
	}
	return candidates, nil
}

func (r candidateLineReviewer) evaluateFactCandidates(ctx context.Context, unit ReviewUnit, candidates []factCandidate) ([]factEvaluation, error) {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	var evaluations []factEvaluation
	_, err := r.capture(ctx, factCandidateEvaluationPrompt, renderEvaluationInput(unit.Text, candidates), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitFactEvaluations),
		SubmitName:   toolSubmitFactEvaluations,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			evaluations, err = decodeFactEvaluationCall(calls)
			if err != nil {
				return err
			}
			return validateEvaluationRefs(factEvaluationRefs(evaluations), refs)
		},
	}, factEvaluationTools())
	if err != nil {
		return nil, fmt.Errorf("evaluate fact candidates: %w", err)
	}
	return evaluations, nil
}

func (r candidateLineReviewer) generateSkillCandidates(ctx context.Context, unit ReviewUnit) ([]skillCandidate, error) {
	var candidates []skillCandidate
	_, err := r.capture(ctx, skillCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillGeneration),
		SubmitName:   toolSubmitSkillGeneration,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			candidates, err = decodeSkillGenerationCall(calls)
			return err
		},
	}, skillGenerationTools())
	if err != nil {
		return nil, fmt.Errorf("generate skill candidates: %w", err)
	}
	for i := range candidates {
		candidates[i].Ref = candidateRef("skill", i)
	}
	return candidates, nil
}

func (r candidateLineReviewer) evaluateSkillCandidates(ctx context.Context, unit ReviewUnit, candidates []skillCandidate) ([]skillEvaluation, error) {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	var evaluations []skillEvaluation
	_, err := r.capture(ctx, skillCandidateEvaluationPrompt, renderEvaluationInput(unit.Text, candidates), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillEvaluations),
		SubmitName:   toolSubmitSkillEvaluations,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			evaluations, err = decodeSkillEvaluationCall(calls)
			if err != nil {
				return err
			}
			return validateEvaluationRefs(skillEvaluationRefs(evaluations), refs)
		},
	}, skillEvaluationTools())
	if err != nil {
		return nil, fmt.Errorf("evaluate skill candidates: %w", err)
	}
	return evaluations, nil
}

func (r candidateLineReviewer) capture(ctx context.Context, system, input string, protocol captureProtocol, tools []ai.ToolDefinition) (captureRunResult, error) {
	return runCaptureWithRetry(ctx,
		func(ctx context.Context) (captureRunResult, error) {
			msg, err := providers.Complete(ctx, r.Model, ai.Context{
				System: system,
				Messages: []ai.Message{
					ai.UserMessage{Content: input},
				},
				Tools: tools,
			}, defaultCandidateCompleteOptions(r.Options), r.Stream)
			if err != nil {
				return captureRunResult{}, err
			}
			if msg.StopReason == ai.StopReasonError && msg.ErrorMessage != "" {
				return captureRunResult{}, fmt.Errorf("provider: %s", msg.ErrorMessage)
			}
			calls := extractAssistantToolCalls(msg)
			calls, err = normalizeCaptureToolCalls(calls)
			if err != nil {
				return captureRunResult{}, err
			}
			return captureRunResult{ToolCalls: calls}, nil
		},
		func(result captureRunResult) error {
			return validateCaptureProtocol(result, protocol)
		},
	)
}

func defaultCandidateCompleteOptions(opts ai.CompleteOptions) ai.CompleteOptions {
	if opts.Timeout == 0 {
		opts.Timeout = reviewerTimeout
	}
	if opts.Temperature == nil {
		temperature := 0.0
		opts.Temperature = &temperature
	}
	return opts
}

func extractAssistantToolCalls(msg ai.AssistantMessage) []ai.ToolCall {
	calls := make([]ai.ToolCall, 0, len(msg.Content))
	for _, block := range msg.Content {
		if call, ok := block.(ai.ToolCall); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func allowedCaptureTools(names ...string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return allowed
}

func decodeFactGenerationCall(calls []ai.ToolCall) ([]factCandidate, error) {
	payload, err := decodeSingleCapturePayload[factGenerationCapturePayload](calls, toolSubmitFactGeneration)
	if err != nil {
		return nil, err
	}
	if err := validateGenerationBatch(len(payload.Candidates), payload.NoCandidateReason); err != nil {
		return nil, err
	}
	if err := validateGeneratedFactCandidates(payload.Candidates); err != nil {
		return nil, err
	}
	return payload.Candidates, nil
}

func decodeFactEvaluationCall(calls []ai.ToolCall) ([]factEvaluation, error) {
	payload, err := decodeSingleCapturePayload[factEvaluationCapturePayload](calls, toolSubmitFactEvaluations)
	if err != nil {
		return nil, err
	}
	if err := validateFactEvaluations(payload.Evaluations); err != nil {
		return nil, err
	}
	return payload.Evaluations, nil
}

func decodeSkillGenerationCall(calls []ai.ToolCall) ([]skillCandidate, error) {
	payload, err := decodeSingleCapturePayload[skillGenerationCapturePayload](calls, toolSubmitSkillGeneration)
	if err != nil {
		return nil, err
	}
	if err := validateGenerationBatch(len(payload.Candidates), payload.NoCandidateReason); err != nil {
		return nil, err
	}
	if err := validateGeneratedSkillCandidates(payload.Candidates); err != nil {
		return nil, err
	}
	return payload.Candidates, nil
}

func decodeSkillEvaluationCall(calls []ai.ToolCall) ([]skillEvaluation, error) {
	payload, err := decodeSingleCapturePayload[skillEvaluationCapturePayload](calls, toolSubmitSkillEvaluations)
	if err != nil {
		return nil, err
	}
	if err := validateSkillEvaluations(payload.Evaluations); err != nil {
		return nil, err
	}
	return payload.Evaluations, nil
}

func validateGeneratedFactCandidates(candidates []factCandidate) error {
	for i, candidate := range candidates {
		if reason := candidate.validateGenerated(); reason != "" {
			return fmt.Errorf("%w: invalid fact candidate %d: %s", errCaptureProtocol, i, reason)
		}
	}
	return nil
}

func validateGeneratedSkillCandidates(candidates []skillCandidate) error {
	for i, candidate := range candidates {
		if reason := candidate.validateGenerated(); reason != "" {
			return fmt.Errorf("%w: invalid skill candidate %d: %s", errCaptureProtocol, i, reason)
		}
	}
	return nil
}

func validateFactEvaluations(evaluations []factEvaluation) error {
	required := []string{
		factScoreEvidenceStrength,
		factScoreSubjectFit,
		factScoreDurability,
		factScoreFutureUtility,
		factScoreAtomicity,
	}
	for i, evaluation := range evaluations {
		if strings.TrimSpace(evaluation.Rationale) == "" {
			return fmt.Errorf("%w: fact evaluation %d missing rationale", errCaptureProtocol, i)
		}
		if err := validateCaptureScores(evaluation.Scores, required); err != nil {
			return fmt.Errorf("%w: fact evaluation %d: %w", errCaptureProtocol, i, err)
		}
	}
	return nil
}

func validateSkillEvaluations(evaluations []skillEvaluation) error {
	required := []string{
		skillScoreEvidenceStrength,
		skillScoreReusableValue,
		skillScoreBaselineSeparation,
		skillScoreProcedureActionability,
		skillScoreApplicabilityClarity,
		skillScoreVerificationQuality,
	}
	for i, evaluation := range evaluations {
		if strings.TrimSpace(evaluation.Rationale) == "" {
			return fmt.Errorf("%w: skill evaluation %d missing rationale", errCaptureProtocol, i)
		}
		if err := validateCaptureScores(evaluation.Scores, required); err != nil {
			return fmt.Errorf("%w: skill evaluation %d: %w", errCaptureProtocol, i, err)
		}
	}
	return nil
}

// validateCaptureScores is the Go-side source of truth for evaluator score
// shape. Provider tool schemas are only the first guardrail.
func validateCaptureScores(scores map[string]int, required []string) error {
	if len(scores) != len(required) {
		return fmt.Errorf("score schema mismatch")
	}
	for _, field := range required {
		score, ok := scores[field]
		if !ok {
			return fmt.Errorf("missing score %q", field)
		}
		if score < 0 || score > maxScoreValue {
			return fmt.Errorf("score %q out of range", field)
		}
	}
	return nil
}

func decodeSingleCapturePayload[T any](calls []ai.ToolCall, name string) (T, error) {
	var payload T
	for _, call := range calls {
		if call.Name != name {
			continue
		}
		return decodeCapturePayload[T](call)
	}
	return payload, fmt.Errorf("%w: missing submit tool %q", errCaptureProtocol, name)
}

func validateGenerationBatch(candidateCount int, noCandidateReason string) error {
	reason := strings.TrimSpace(noCandidateReason)
	if candidateCount == 0 {
		if reason == "" {
			return fmt.Errorf("%w: empty generation batch missing no_candidate_reason", errCaptureProtocol)
		}
		return nil
	}
	if reason != "" {
		return fmt.Errorf("%w: generation batch with candidates must omit no_candidate_reason", errCaptureProtocol)
	}
	return nil
}

func validateEvaluationRefs(got, expected []CandidateRef) error {
	expectedSet := make(map[CandidateRef]struct{}, len(expected))
	for _, ref := range expected {
		expectedSet[ref] = struct{}{}
	}

	// Evaluators must score exactly the host-assigned refs; missing, duplicate,
	// or model-invented refs fail closed before gate scoring.
	seen := make(map[CandidateRef]struct{}, len(got))
	for _, ref := range got {
		if _, ok := expectedSet[ref]; !ok {
			return fmt.Errorf("%w: unknown candidate_ref %q", errCaptureProtocol, ref)
		}
		if _, ok := seen[ref]; ok {
			return fmt.Errorf("%w: duplicate candidate_ref %q", errCaptureProtocol, ref)
		}
		seen[ref] = struct{}{}
	}

	for _, ref := range expected {
		if _, ok := seen[ref]; !ok {
			return fmt.Errorf("%w: missing evaluation for candidate_ref %q", errCaptureProtocol, ref)
		}
	}
	return nil
}

func factEvaluationRefs(evaluations []factEvaluation) []CandidateRef {
	refs := make([]CandidateRef, 0, len(evaluations))
	for _, evaluation := range evaluations {
		refs = append(refs, evaluation.Ref)
	}
	return refs
}

func skillEvaluationRefs(evaluations []skillEvaluation) []CandidateRef {
	refs := make([]CandidateRef, 0, len(evaluations))
	for _, evaluation := range evaluations {
		refs = append(refs, evaluation.Ref)
	}
	return refs
}

func acceptedFactCandidates(candidates []factCandidate, decisions []CandidateGateDecision) []factCandidate {
	accepted := acceptedRefSet(decisions)
	out := make([]factCandidate, 0, len(decisions))
	for _, candidate := range candidates {
		if _, ok := accepted[candidate.Ref]; ok {
			out = append(out, candidate)
		}
	}
	return out
}

func acceptedSkillCandidates(candidates []skillCandidate, decisions []CandidateGateDecision) []skillCandidate {
	accepted := acceptedRefSet(decisions)
	out := make([]skillCandidate, 0, len(decisions))
	for _, candidate := range candidates {
		if _, ok := accepted[candidate.Ref]; ok {
			out = append(out, candidate)
		}
	}
	return out
}

func acceptedRefSet(decisions []CandidateGateDecision) map[CandidateRef]struct{} {
	accepted := make(map[CandidateRef]struct{}, len(decisions))
	for _, decision := range decisions {
		accepted[decision.Ref] = struct{}{}
	}
	return accepted
}

func renderEvaluationInput[T any](reviewText string, candidates []T) string {
	var b strings.Builder
	b.WriteString(reviewText)
	b.WriteString("\n\n<candidates_json>\n")
	// encoding/json keeps candidate free text inside JSON strings and escapes
	// '<'/'>' as \u003c/\u003e, so generated marker text cannot close this block.
	data, _ := json.MarshalIndent(candidates, "", "  ")
	b.Write(data)
	b.WriteString("\n</candidates_json>\n")
	return b.String()
}
