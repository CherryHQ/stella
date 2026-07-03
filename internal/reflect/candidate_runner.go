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
	toolSubmitFactCandidate  = "submit_fact_candidate"
	toolFinishFactGeneration = "finish_fact_generation"
	toolSubmitFactEvaluation = "submit_fact_evaluation"
	toolFinishFactEvaluation = "finish_fact_evaluation"

	toolSubmitSkillCandidate  = "submit_skill_candidate"
	toolFinishSkillGeneration = "finish_skill_generation"
	toolSubmitSkillEvaluation = "submit_skill_evaluation"
	toolFinishSkillEvaluation = "finish_skill_evaluation"
)

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
	result, err := r.capture(ctx, factCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitFactCandidate, toolFinishFactGeneration),
		FinishName:   toolFinishFactGeneration,
	}, factGenerationTools())
	if err != nil {
		return nil, fmt.Errorf("generate fact candidates: %w", err)
	}
	candidates, err := decodeFactCandidateCalls(result.ToolCalls)
	if err != nil {
		return nil, err
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
	result, err := r.capture(ctx, factCandidateEvaluationPrompt, renderEvaluationInput(unit.Text, candidates), captureProtocol{
		AllowedTools:   allowedCaptureTools(toolSubmitFactEvaluation, toolFinishFactEvaluation),
		FinishName:     toolFinishFactEvaluation,
		EvaluationName: toolSubmitFactEvaluation,
		ExpectedRefs:   refs,
	}, factEvaluationTools())
	if err != nil {
		return nil, fmt.Errorf("evaluate fact candidates: %w", err)
	}
	return decodeFactEvaluationCalls(result.ToolCalls)
}

func (r candidateLineReviewer) generateSkillCandidates(ctx context.Context, unit ReviewUnit) ([]skillCandidate, error) {
	result, err := r.capture(ctx, skillCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillCandidate, toolFinishSkillGeneration),
		FinishName:   toolFinishSkillGeneration,
	}, skillGenerationTools())
	if err != nil {
		return nil, fmt.Errorf("generate skill candidates: %w", err)
	}
	candidates, err := decodeSkillCandidateCalls(result.ToolCalls)
	if err != nil {
		return nil, err
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
	result, err := r.capture(ctx, skillCandidateEvaluationPrompt, renderEvaluationInput(unit.Text, candidates), captureProtocol{
		AllowedTools:   allowedCaptureTools(toolSubmitSkillEvaluation, toolFinishSkillEvaluation),
		FinishName:     toolFinishSkillEvaluation,
		EvaluationName: toolSubmitSkillEvaluation,
		ExpectedRefs:   refs,
	}, skillEvaluationTools())
	if err != nil {
		return nil, fmt.Errorf("evaluate skill candidates: %w", err)
	}
	return decodeSkillEvaluationCalls(result.ToolCalls)
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

func decodeFactCandidateCalls(calls []ai.ToolCall) ([]factCandidate, error) {
	var candidates []factCandidate
	for _, call := range calls {
		if call.Name != toolSubmitFactCandidate {
			continue
		}
		candidate, err := decodeCapturePayload[factCandidate](call)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func decodeFactEvaluationCalls(calls []ai.ToolCall) ([]factEvaluation, error) {
	var evaluations []factEvaluation
	for _, call := range calls {
		if call.Name != toolSubmitFactEvaluation {
			continue
		}
		evaluation, err := decodeCapturePayload[factEvaluation](call)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, nil
}

func decodeSkillCandidateCalls(calls []ai.ToolCall) ([]skillCandidate, error) {
	var candidates []skillCandidate
	for _, call := range calls {
		if call.Name != toolSubmitSkillCandidate {
			continue
		}
		candidate, err := decodeCapturePayload[skillCandidate](call)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func decodeSkillEvaluationCalls(calls []ai.ToolCall) ([]skillEvaluation, error) {
	var evaluations []skillEvaluation
	for _, call := range calls {
		if call.Name != toolSubmitSkillEvaluation {
			continue
		}
		evaluation, err := decodeCapturePayload[skillEvaluation](call)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, nil
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
	data, _ := json.MarshalIndent(candidates, "", "  ")
	b.Write(data)
	b.WriteString("\n</candidates_json>\n")
	return b.String()
}
