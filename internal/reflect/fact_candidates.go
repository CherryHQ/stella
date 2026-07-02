package reflect

import "strings"

const (
	factScoreEvidenceStrength = "evidence_strength"
	factScoreSubjectFit       = "subject_fit"
	factScoreDurability       = "durability"
	factScoreFutureUtility    = "future_utility"
	factScoreAtomicity        = "atomicity"

	factSubjectCap = 3
)

type factSubject string

const (
	factSubjectUser  factSubject = "user"
	factSubjectAgent factSubject = "agent"
	factSubjectWorld factSubject = "world"
)

type factEvidenceSource string

const (
	factEvidenceUserMessage          factEvidenceSource = "user_message"
	factEvidenceUserCorrection       factEvidenceSource = "user_correction"
	factEvidenceToolResult           factEvidenceSource = "tool_result"
	factEvidenceAgentSoulInstruction factEvidenceSource = "agent_soul_instruction"
)

type factCandidate struct {
	Ref            CandidateRef
	Subject        factSubject
	Content        string
	Evidence       []factEvidence
	ExpectedEffect string
	HandoffHints   factHandoffHints
}

type factEvidence struct {
	SourceType factEvidenceSource
	Source     string
	Reason     string
}

type factHandoffHints struct {
	KnowledgeSearchQueryHint string
}

type factEvaluation struct {
	Ref       CandidateRef
	Scores    map[string]int
	Rationale string
}

type factGateOptions struct {
	PrivateOneToOne bool
}

func gateFactCandidates(candidates []factCandidate, evaluations []factEvaluation, opts factGateOptions) CandidateGateResult {
	evals := make(map[CandidateRef]factEvaluation, len(evaluations))
	var result CandidateGateResult
	for _, evaluation := range evaluations {
		evals[evaluation.Ref] = evaluation
	}

	inputsBySubject := map[factSubject][]CandidateGateInput{}
	for _, candidate := range candidates {
		evaluation, hasEvaluation := evals[candidate.Ref]
		decision := CandidateGateDecision{Ref: candidate.Ref}
		if hasEvaluation {
			decision.Scores = evaluation.Scores
		}

		if reason := candidate.validate(opts); reason != "" {
			decision.Reason = reason
			result.Rejected = append(result.Rejected, decision)
			continue
		}
		if !hasEvaluation {
			decision.Reason = rejectSchemaMissingField
			result.Rejected = append(result.Rejected, decision)
			continue
		}
		if evaluation.Scores[factScoreSubjectFit] < factSubjectFitFloor(candidate.Subject) {
			decision.Reason = rejectScoreFloorFailed
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		inputsBySubject[candidate.Subject] = append(inputsBySubject[candidate.Subject], CandidateGateInput{
			Ref:     candidate.Ref,
			Content: candidate.gateText(),
			Scores:  evaluation.Scores,
		})
	}

	for _, subject := range []factSubject{factSubjectUser, factSubjectAgent, factSubjectWorld} {
		subjectResult := gateCandidates(inputsBySubject[subject], factGateConfig(factSubjectCap))
		result.Accepted = append(result.Accepted, subjectResult.Accepted...)
		result.Rejected = append(result.Rejected, subjectResult.Rejected...)
	}
	return result
}

func factGateConfig(cap int) CandidateGateConfig {
	return CandidateGateConfig{
		Weights: map[string]float64{
			factScoreEvidenceStrength: 0.20,
			factScoreSubjectFit:       0.20,
			factScoreDurability:       0.20,
			factScoreFutureUtility:    0.20,
			factScoreAtomicity:        0.20,
		},
		CoreFields: []string{
			factScoreEvidenceStrength,
			factScoreSubjectFit,
			factScoreDurability,
			factScoreFutureUtility,
			factScoreAtomicity,
		},
		CoreFloor:      2,
		Threshold:      0.70,
		Cap:            cap,
		TieBreakFields: []string{factScoreSubjectFit},
	}
}

func factSubjectFitFloor(subject factSubject) int {
	if subject == factSubjectUser || subject == factSubjectAgent {
		return 3
	}
	return 2
}

func (c factCandidate) validate(opts factGateOptions) RejectReason {
	switch c.Subject {
	case factSubjectUser, factSubjectAgent:
		if !opts.PrivateOneToOne {
			return rejectScopeNotEligible
		}
		if strings.TrimSpace(c.HandoffHints.KnowledgeSearchQueryHint) != "" {
			return rejectSchemaMissingField
		}
	case factSubjectWorld:
		if strings.TrimSpace(c.HandoffHints.KnowledgeSearchQueryHint) == "" {
			return rejectSchemaMissingField
		}
	default:
		return rejectSchemaMissingField
	}

	if c.Ref == "" || strings.TrimSpace(c.Content) == "" || strings.TrimSpace(c.ExpectedEffect) == "" {
		return rejectSchemaMissingField
	}
	if len(c.Evidence) == 0 {
		return rejectSchemaMissingField
	}
	for _, evidence := range c.Evidence {
		if reason := evidence.validate(); reason != "" {
			return reason
		}
	}
	return ""
}

func (e factEvidence) validate() RejectReason {
	switch e.SourceType {
	case factEvidenceUserMessage, factEvidenceUserCorrection, factEvidenceAgentSoulInstruction:
	case factEvidenceToolResult:
		if !strings.Contains(e.Source, "[tool_result_summary]") {
			return rejectSchemaMissingField
		}
	default:
		return rejectSchemaMissingField
	}
	if strings.TrimSpace(e.Source) == "" || strings.TrimSpace(e.Reason) == "" {
		return rejectSchemaMissingField
	}
	return ""
}

func (c factCandidate) gateText() string {
	var b strings.Builder
	b.WriteString(c.Content)
	b.WriteString("\n")
	b.WriteString(c.ExpectedEffect)
	b.WriteString("\n")
	b.WriteString(c.HandoffHints.KnowledgeSearchQueryHint)
	for _, evidence := range c.Evidence {
		b.WriteString("\n")
		b.WriteString(string(evidence.SourceType))
		b.WriteString(" ")
		b.WriteString(evidence.Source)
		b.WriteString(" ")
		b.WriteString(evidence.Reason)
	}
	return b.String()
}
