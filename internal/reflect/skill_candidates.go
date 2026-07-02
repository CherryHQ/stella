package reflect

import "strings"

const (
	skillScoreEvidenceStrength       = "evidence_strength"
	skillScoreReusableValue          = "reusable_value"
	skillScoreBaselineSeparation     = "baseline_separation"
	skillScoreProcedureActionability = "procedure_actionability"
	skillScoreApplicabilityClarity   = "applicability_clarity"
	skillScoreVerificationQuality    = "verification_quality"

	skillCandidateCap = 2
)

type skillSignal string

const (
	skillSignalUserCorrection      skillSignal = "user_correction"
	skillSignalSuccessfulWorkflow  skillSignal = "successful_workflow"
	skillSignalFailureRecovery     skillSignal = "failure_recovery"
	skillSignalToolingDiscovery    skillSignal = "tooling_discovery"
	skillSignalExplicitInstruction skillSignal = "explicit_instruction"
	skillSignalSkillGap            skillSignal = "skill_gap"
)

type skillCandidate struct {
	Ref                 CandidateRef
	Learning            skillLearning
	Evidence            []skillEvidence
	Applicability       skillApplicability
	Procedure           skillProcedure
	SessionSkillContext *sessionSkillContext
	HandoffHints        skillHandoffHints
}

type skillLearning struct {
	Summary       string
	ReusableDelta string
}

type skillEvidence struct {
	SignalType skillSignal
	Source     string
	Reason     string
}

type skillApplicability struct {
	TriggerExamples    []string
	NonTriggerExamples []string
}

type skillProcedure struct {
	Prerequisites  []string
	Steps          []string
	DecisionPoints []string
	Pitfalls       []string
	Verification   []string
}

type sessionSkillContext struct {
	UsedSkillRefs            []string
	ChangeAgainstLoadedSkill string
}

type skillHandoffHints struct {
	SearchQueryHint string
}

type skillEvaluation struct {
	Ref       CandidateRef
	Scores    map[string]int
	Rationale string
}

func gateSkillCandidates(candidates []skillCandidate, evaluations []skillEvaluation) CandidateGateResult {
	evals := make(map[CandidateRef]skillEvaluation, len(evaluations))
	var result CandidateGateResult
	for _, evaluation := range evaluations {
		evals[evaluation.Ref] = evaluation
	}

	inputs := make([]CandidateGateInput, 0, len(candidates))
	for _, candidate := range candidates {
		evaluation, hasEvaluation := evals[candidate.Ref]
		decision := CandidateGateDecision{Ref: candidate.Ref}
		if hasEvaluation {
			decision.Scores = evaluation.Scores
		}

		if reason := candidate.validate(); reason != "" {
			decision.Reason = reason
			result.Rejected = append(result.Rejected, decision)
			continue
		}
		if !hasEvaluation {
			decision.Reason = rejectSchemaMissingField
			result.Rejected = append(result.Rejected, decision)
			continue
		}
		if failsSkillSoftFloor(evaluation.Scores) {
			decision.Reason = rejectScoreFloorFailed
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		inputs = append(inputs, CandidateGateInput{
			Ref:     candidate.Ref,
			Content: candidate.gateText(),
			Scores:  evaluation.Scores,
		})
	}

	gated := gateCandidates(inputs, skillGateConfig(skillCandidateCap))
	result.Accepted = append(result.Accepted, gated.Accepted...)
	result.Rejected = append(result.Rejected, gated.Rejected...)
	return result
}

func skillGateConfig(cap int) CandidateGateConfig {
	return CandidateGateConfig{
		Weights: map[string]float64{
			skillScoreEvidenceStrength:       0.20,
			skillScoreReusableValue:          0.24,
			skillScoreBaselineSeparation:     0.16,
			skillScoreProcedureActionability: 0.16,
			skillScoreApplicabilityClarity:   0.14,
			skillScoreVerificationQuality:    0.10,
		},
		CoreFields: []string{
			skillScoreEvidenceStrength,
			skillScoreReusableValue,
			skillScoreBaselineSeparation,
			skillScoreProcedureActionability,
		},
		CoreFloor:      2,
		Threshold:      0.70,
		Cap:            cap,
		TieBreakFields: []string{skillScoreEvidenceStrength},
	}
}

func failsSkillSoftFloor(scores map[string]int) bool {
	return scores[skillScoreApplicabilityClarity] < 1 || scores[skillScoreVerificationQuality] < 1
}

func (c skillCandidate) validate() RejectReason {
	if c.Ref == "" ||
		strings.TrimSpace(c.Learning.Summary) == "" ||
		strings.TrimSpace(c.Learning.ReusableDelta) == "" ||
		strings.TrimSpace(c.HandoffHints.SearchQueryHint) == "" {
		return rejectSchemaMissingField
	}
	if len(c.Evidence) == 0 ||
		!hasNonEmpty(c.Applicability.TriggerExamples) ||
		!hasNonEmpty(c.Applicability.NonTriggerExamples) ||
		!hasNonEmpty(c.Procedure.Steps) ||
		!hasNonEmpty(c.Procedure.Verification) {
		return rejectSchemaMissingField
	}
	for _, evidence := range c.Evidence {
		if reason := evidence.validate(); reason != "" {
			return reason
		}
	}
	if c.SessionSkillContext != nil {
		if !hasNonEmpty(c.SessionSkillContext.UsedSkillRefs) ||
			strings.TrimSpace(c.SessionSkillContext.ChangeAgainstLoadedSkill) == "" {
			return rejectSchemaMissingField
		}
	}
	return ""
}

func (e skillEvidence) validate() RejectReason {
	switch e.SignalType {
	case skillSignalUserCorrection,
		skillSignalSuccessfulWorkflow,
		skillSignalFailureRecovery,
		skillSignalToolingDiscovery,
		skillSignalExplicitInstruction,
		skillSignalSkillGap:
	default:
		return rejectSchemaMissingField
	}
	if strings.TrimSpace(e.Source) == "" || strings.TrimSpace(e.Reason) == "" {
		return rejectSchemaMissingField
	}
	return ""
}

func (c skillCandidate) gateText() string {
	var b strings.Builder
	b.WriteString(c.Learning.Summary)
	b.WriteString("\n")
	b.WriteString(c.Learning.ReusableDelta)
	b.WriteString("\n")
	b.WriteString(c.HandoffHints.SearchQueryHint)
	for _, evidence := range c.Evidence {
		b.WriteString("\n")
		b.WriteString(string(evidence.SignalType))
		b.WriteString(" ")
		b.WriteString(evidence.Source)
		b.WriteString(" ")
		b.WriteString(evidence.Reason)
	}
	for _, value := range c.Applicability.TriggerExamples {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Applicability.NonTriggerExamples {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.Steps {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.Verification {
		b.WriteString("\n")
		b.WriteString(value)
	}
	return b.String()
}

func hasNonEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
