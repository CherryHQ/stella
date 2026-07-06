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
	Ref            CandidateRef     `json:"candidate_ref,omitempty"`
	Subject        factSubject      `json:"subject"`
	Content        string           `json:"content"`
	Evidence       []factEvidence   `json:"evidence"`
	ExpectedEffect string           `json:"expected_effect"`
	HandoffHints   factHandoffHints `json:"handoff_hints"`
}

type factEvidence struct {
	SourceType factEvidenceSource `json:"source_type"`
	Source     string             `json:"source"`
	Reason     string             `json:"reason"`
}

type factHandoffHints struct {
	KnowledgeSearchQueryHint string `json:"knowledge_search_query_hint,omitempty"`
}

type factEvaluation struct {
	Ref       CandidateRef   `json:"candidate_ref"`
	Scores    map[string]int `json:"scores"`
	Rationale string         `json:"rationale"`
}

type factGateOptions struct {
	PrivateOneToOne bool
}

func gateFactCandidates(candidates []factCandidate, evaluations []factEvaluation, opts factGateOptions) CandidateGateResult {
	return gateFactCandidatesWithSettings(candidates, evaluations, opts, CandidateGateSettings{})
}

func gateFactCandidatesWithSettings(candidates []factCandidate, evaluations []factEvaluation, opts factGateOptions, settings CandidateGateSettings) CandidateGateResult {
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
		subjectResult := gateCandidates(inputsBySubject[subject], factGateConfig(settings))
		result.Accepted = append(result.Accepted, subjectResult.Accepted...)
		result.Rejected = append(result.Rejected, subjectResult.Rejected...)
	}
	return result
}

func factGateConfig(settings CandidateGateSettings) CandidateGateConfig {
	settings = settings.withDefaults()
	return CandidateGateConfig{
		Weights: settings.FactWeights,
		CoreFields: []string{
			factScoreEvidenceStrength,
			factScoreSubjectFit,
			factScoreDurability,
			factScoreFutureUtility,
			factScoreAtomicity,
		},
		CoreFloor:      2,
		Threshold:      settings.FactThreshold,
		Cap:            settings.FactSubjectCap,
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
	if c.Ref == "" {
		return rejectSchemaMissingField
	}
	return c.validateContent(opts)
}

func (c factCandidate) validateGenerated() RejectReason {
	if c.Ref != "" {
		return rejectSchemaExtraField
	}
	return c.validateContent(factGateOptions{PrivateOneToOne: true})
}

func (c factCandidate) validateContent(opts factGateOptions) RejectReason {
	switch c.Subject {
	case factSubjectUser, factSubjectAgent:
		if !opts.PrivateOneToOne {
			return rejectScopeNotEligible
		}
		if strings.TrimSpace(c.HandoffHints.KnowledgeSearchQueryHint) != "" {
			return rejectSchemaExtraField
		}
	case factSubjectWorld:
		if strings.TrimSpace(c.HandoffHints.KnowledgeSearchQueryHint) == "" {
			return rejectSchemaMissingField
		}
	default:
		return rejectSchemaMissingField
	}

	if strings.TrimSpace(c.Content) == "" || strings.TrimSpace(c.ExpectedEffect) == "" {
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
