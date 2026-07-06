package reflect

// CandidateGateSettings keeps #532 score gates configurable without exposing a
// persistent confidence field or coupling thresholds to prompt text.
type CandidateGateSettings struct {
	FactWeights       map[string]float64
	FactThreshold     float64
	FactSubjectCap    int
	SkillWeights      map[string]float64
	SkillThreshold    float64
	SkillCandidateCap int
}

func defaultCandidateGateSettings() CandidateGateSettings {
	return CandidateGateSettings{
		FactWeights: map[string]float64{
			factScoreEvidenceStrength: 0.20,
			factScoreSubjectFit:       0.20,
			factScoreDurability:       0.20,
			factScoreFutureUtility:    0.20,
			factScoreAtomicity:        0.20,
		},
		FactThreshold:  0.70,
		FactSubjectCap: factSubjectCap,
		SkillWeights: map[string]float64{
			skillScoreEvidenceStrength:       0.20,
			skillScoreReusableValue:          0.24,
			skillScoreBaselineSeparation:     0.16,
			skillScoreProcedureActionability: 0.16,
			skillScoreApplicabilityClarity:   0.14,
			skillScoreVerificationQuality:    0.10,
		},
		SkillThreshold:    0.80,
		SkillCandidateCap: skillCandidateCap,
	}
}

func (s CandidateGateSettings) withDefaults() CandidateGateSettings {
	defaults := defaultCandidateGateSettings()
	if len(s.FactWeights) == 0 {
		s.FactWeights = defaults.FactWeights
	}
	if s.FactThreshold == 0 {
		s.FactThreshold = defaults.FactThreshold
	}
	if s.FactSubjectCap == 0 {
		s.FactSubjectCap = defaults.FactSubjectCap
	}
	if len(s.SkillWeights) == 0 {
		s.SkillWeights = defaults.SkillWeights
	}
	if s.SkillThreshold == 0 {
		s.SkillThreshold = defaults.SkillThreshold
	}
	if s.SkillCandidateCap == 0 {
		s.SkillCandidateCap = defaults.SkillCandidateCap
	}
	return s
}
