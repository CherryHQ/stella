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
	Ref                 CandidateRef         `json:"candidate_ref,omitempty"`
	Learning            skillLearning        `json:"learning"`
	Evidence            []skillEvidence      `json:"evidence"`
	Applicability       skillApplicability   `json:"applicability"`
	Procedure           skillProcedure       `json:"procedure"`
	SessionSkillContext *sessionSkillContext `json:"session_skill_context,omitempty"`
	HandoffHints        skillHandoffHints    `json:"handoff_hints"`
}

type skillLearning struct {
	Summary       string `json:"summary"`
	ReusableDelta string `json:"reusable_delta"`
}

type skillEvidence struct {
	SignalType skillSignal `json:"signal_type"`
	Source     string      `json:"source"`
	Reason     string      `json:"reason"`
}

type skillApplicability struct {
	TriggerExamples    []string `json:"trigger_examples"`
	NonTriggerExamples []string `json:"non_trigger_examples"`
}

type skillProcedure struct {
	Prerequisites  []string `json:"prerequisites,omitempty"`
	Steps          []string `json:"steps"`
	DecisionPoints []string `json:"decision_points,omitempty"`
	Pitfalls       []string `json:"pitfalls,omitempty"`
	Verification   []string `json:"verification"`
}

type sessionSkillContext struct {
	UsedSkillRefs            []string `json:"used_skill_refs"`
	ChangeAgainstLoadedSkill string   `json:"change_against_loaded_skill"`
}

type skillHandoffHints struct {
	SearchQueryHint string `json:"search_query_hint"`
}

type skillEvaluation struct {
	Ref       CandidateRef   `json:"candidate_ref"`
	Scores    map[string]int `json:"scores"`
	Rationale string         `json:"rationale"`
}

func gateSkillCandidates(candidates []skillCandidate, evaluations []skillEvaluation) CandidateGateResult {
	return gateSkillCandidatesWithSettings(candidates, evaluations, CandidateGateSettings{})
}

func gateSkillCandidatesWithSettings(candidates []skillCandidate, evaluations []skillEvaluation, settings CandidateGateSettings) CandidateGateResult {
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

	gated := gateCandidates(inputs, skillGateConfig(settings))
	result.Accepted = append(result.Accepted, gated.Accepted...)
	result.Rejected = append(result.Rejected, gated.Rejected...)
	return result
}

func skillGateConfig(settings CandidateGateSettings) CandidateGateConfig {
	settings = settings.withDefaults()
	return CandidateGateConfig{
		Weights: settings.SkillWeights,
		CoreFields: []string{
			skillScoreEvidenceStrength,
			skillScoreReusableValue,
			skillScoreBaselineSeparation,
			skillScoreProcedureActionability,
		},
		CoreFloor:      2,
		Threshold:      settings.SkillThreshold,
		Cap:            settings.SkillCandidateCap,
		TieBreakFields: []string{skillScoreEvidenceStrength},
	}
}

func failsSkillSoftFloor(scores map[string]int) bool {
	return scores[skillScoreApplicabilityClarity] < 1 || scores[skillScoreVerificationQuality] < 1
}

func (c skillCandidate) validate() RejectReason {
	if c.Ref == "" {
		return rejectSchemaMissingField
	}
	return c.validateContent()
}

func (c skillCandidate) validateGenerated() RejectReason {
	if c.Ref != "" {
		return rejectSchemaExtraField
	}
	return c.validateContent()
}

func (c skillCandidate) validateContent() RejectReason {
	if strings.TrimSpace(c.Learning.Summary) == "" ||
		strings.TrimSpace(c.Learning.ReusableDelta) == "" ||
		strings.TrimSpace(c.HandoffHints.SearchQueryHint) == "" {
		return rejectSchemaMissingField
	}
	if len(c.Evidence) == 0 ||
		!hasRequiredNonEmpty(c.Applicability.TriggerExamples) ||
		!hasRequiredNonEmpty(c.Applicability.NonTriggerExamples) ||
		!hasRequiredNonEmpty(c.Procedure.Steps) ||
		!hasRequiredNonEmpty(c.Procedure.Verification) ||
		!hasOptionalNonEmpty(c.Procedure.Prerequisites) ||
		!hasOptionalNonEmpty(c.Procedure.DecisionPoints) ||
		!hasOptionalNonEmpty(c.Procedure.Pitfalls) {
		return rejectSchemaMissingField
	}
	for _, evidence := range c.Evidence {
		if reason := evidence.validate(); reason != "" {
			return reason
		}
	}
	if c.SessionSkillContext != nil {
		if !hasRequiredNonEmpty(c.SessionSkillContext.UsedSkillRefs) ||
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
	// Keep every model-controlled string persisted from skillCandidate in this
	// projection. The final provenance envelope is scanned again independently.
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
	for _, value := range c.Procedure.Prerequisites {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.Steps {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.DecisionPoints {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.Pitfalls {
		b.WriteString("\n")
		b.WriteString(value)
	}
	for _, value := range c.Procedure.Verification {
		b.WriteString("\n")
		b.WriteString(value)
	}
	if c.SessionSkillContext != nil {
		for _, value := range c.SessionSkillContext.UsedSkillRefs {
			b.WriteString("\n")
			b.WriteString(value)
		}
		b.WriteString("\n")
		b.WriteString(c.SessionSkillContext.ChangeAgainstLoadedSkill)
	}
	return b.String()
}

func hasRequiredNonEmpty(values []string) bool {
	return len(values) > 0 && hasOptionalNonEmpty(values)
}

func hasOptionalNonEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}
