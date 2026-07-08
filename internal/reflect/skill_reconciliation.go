package reflect

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type skillWritePlanOperation string

const (
	skillOperationNoop   skillWritePlanOperation = "noop"
	skillOperationCreate skillWritePlanOperation = "create_skill"
	skillOperationPatch  skillWritePlanOperation = "patch_skill"
)

type skillRelatedBundle struct {
	Candidates     []skillCandidate        `json:"candidates"`
	RelatedRecords []skillRelatedRecord    `json:"related_records,omitempty"`
	RelationHints  []skillRelatedSelection `json:"relation_hints,omitempty"`
	Limits         relatedBundleLimits     `json:"limits"`
}

type skillRelatedRecord struct {
	Skill           pkgplugins.Skill `json:"skill"`
	MainFileContent string           `json:"main_file_content"`
}

type skillReconciliationPlan struct {
	Operations []skillWriteOperation `json:"operations,omitempty"`
}

type skillWriteOperation struct {
	Operation            skillWritePlanOperation `json:"operation"`
	CandidateRefs        []CandidateRef          `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs []CandidateRef          `json:"covered_candidate_refs,omitempty"`
	TargetSkillID        string                  `json:"target_skill_id,omitempty"`
	ExpectedSkillVersion int64                   `json:"expected_skill_version,omitempty"`
	Name                 string                  `json:"name,omitempty"`
	Description          string                  `json:"description,omitempty"`
	MainFileContent      string                  `json:"main_file_content,omitempty"`
	Rationale            string                  `json:"rationale,omitempty"`
}

func validateSkillReconciliationPlan(bundle skillRelatedBundle, plan skillReconciliationPlan) error {
	coverage := newCandidateCoverage("skill", skillRefsFromCandidates(bundle.Candidates))
	related := make(map[string]skillRelatedRecord, len(bundle.RelatedRecords))
	for _, record := range bundle.RelatedRecords {
		related[record.Skill.ID] = record
	}
	allowedRefs := skillRefsFromCandidates(bundle.Candidates)
	for _, op := range plan.Operations {
		if !validSkillWriteOperation(op.Operation) {
			return fmt.Errorf("skill reconciliation: invalid operation %q", op.Operation)
		}
		if err := coverage.markAll("skill", allowedRefs, op.CandidateRefs, op.CoveredCandidateRefs); err != nil {
			return err
		}
		switch op.Operation {
		case skillOperationNoop:
			if op.TargetSkillID != "" || op.Name != "" || op.Description != "" || op.MainFileContent != "" {
				return fmt.Errorf("skill reconciliation: noop cannot write")
			}
		case skillOperationCreate:
			if op.TargetSkillID != "" {
				return fmt.Errorf("skill reconciliation: create_skill cannot target an existing skill")
			}
			if strings.TrimSpace(op.Name) == "" || strings.TrimSpace(op.Description) == "" || strings.TrimSpace(op.MainFileContent) == "" {
				return fmt.Errorf("skill reconciliation: create_skill requires name, description, and SKILL.md")
			}
		case skillOperationPatch:
			record, ok := related[op.TargetSkillID]
			if !ok {
				return fmt.Errorf("skill reconciliation: patch target %q is not in related bundle", op.TargetSkillID)
			}
			if !isReflectOwnedActiveUserAgentSkill(record.Skill) {
				return fmt.Errorf("skill reconciliation: patch target %q is not active reflect-owned user_agent", op.TargetSkillID)
			}
			if op.ExpectedSkillVersion <= 0 || op.ExpectedSkillVersion != record.Skill.Version {
				return fmt.Errorf("skill reconciliation: stale expected version for %q", op.TargetSkillID)
			}
			if strings.TrimSpace(op.MainFileContent) == "" {
				return fmt.Errorf("skill reconciliation: patch_skill requires SKILL.md")
			}
		}
	}
	return coverage.requireComplete()
}

func validSkillWriteOperation(op skillWritePlanOperation) bool {
	switch op {
	case skillOperationNoop, skillOperationCreate, skillOperationPatch:
		return true
	default:
		return false
	}
}

func skillRefsFromCandidates(candidates []skillCandidate) []CandidateRef {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	return refs
}

func isReflectOwnedActiveUserAgentSkill(skill pkgplugins.Skill) bool {
	if skill.Scope != "user_agent" || skill.Status != "active" {
		return false
	}
	var metadata struct {
		CreatedBy string `json:"created_by"`
	}
	if len(skill.Metadata) == 0 || json.Unmarshal(skill.Metadata, &metadata) != nil {
		return false
	}
	return metadata.CreatedBy == "reflect"
}
