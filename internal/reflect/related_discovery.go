package reflect

import (
	"fmt"
)

type knowledgeRelationKind string

const (
	knowledgeRelationEquivalent      knowledgeRelationKind = "equivalent"
	knowledgeRelationConflict        knowledgeRelationKind = "conflict"
	knowledgeRelationSupersedes      knowledgeRelationKind = "supersedes"
	knowledgeRelationDependsOn       knowledgeRelationKind = "depends_on"
	knowledgeRelationPossiblyAffects knowledgeRelationKind = "possibly_affects"
	knowledgeRelationSameEntitySlot  knowledgeRelationKind = "same_entity_or_slot"
)

type skillRelationKind string

const (
	skillRelationSameWorkflow       skillRelationKind = "same_workflow"
	skillRelationOverlappingTrigger skillRelationKind = "overlapping_trigger"
	skillRelationBroaderWorkflow    skillRelationKind = "broader_workflow"
	skillRelationNarrowerWorkflow   skillRelationKind = "narrower_workflow"
	skillRelationPatchableGap       skillRelationKind = "patchable_gap"
	skillRelationStalePredecessor   skillRelationKind = "stale_predecessor"
)

type knowledgeRelatedSelection struct {
	CandidateRef CandidateRef           `json:"candidate_ref"`
	Related      []knowledgeRelatedHint `json:"related"`
	Reason       string                 `json:"reason,omitempty"`
}

type knowledgeRelatedHint struct {
	FactID   string                `json:"fact_id"`
	Relation knowledgeRelationKind `json:"relation"`
}

type skillRelatedSelection struct {
	CandidateRef CandidateRef       `json:"candidate_ref"`
	Related      []skillRelatedHint `json:"related"`
	Reason       string             `json:"reason,omitempty"`
}

type skillRelatedHint struct {
	SkillID  string            `json:"skill_id"`
	Relation skillRelationKind `json:"relation"`
}

func normalizeKnowledgeRelatedSelections(selections []knowledgeRelatedSelection) []knowledgeRelatedSelection {
	out := make([]knowledgeRelatedSelection, 0, len(selections))
	positions := make(map[CandidateRef]int, len(selections))
	for _, selection := range selections {
		if idx, ok := positions[selection.CandidateRef]; ok {
			out[idx].Related = append(out[idx].Related, selection.Related...)
			if out[idx].Reason == "" {
				out[idx].Reason = selection.Reason
			}
			continue
		}
		positions[selection.CandidateRef] = len(out)
		selection.Related = append([]knowledgeRelatedHint(nil), selection.Related...)
		out = append(out, selection)
	}
	for i := range out {
		out[i].Related = dedupeKnowledgeRelatedHints(out[i].Related)
	}
	return out
}

func normalizeSkillRelatedSelections(selections []skillRelatedSelection) []skillRelatedSelection {
	out := make([]skillRelatedSelection, 0, len(selections))
	positions := make(map[CandidateRef]int, len(selections))
	for _, selection := range selections {
		if idx, ok := positions[selection.CandidateRef]; ok {
			out[idx].Related = append(out[idx].Related, selection.Related...)
			if out[idx].Reason == "" {
				out[idx].Reason = selection.Reason
			}
			continue
		}
		positions[selection.CandidateRef] = len(out)
		selection.Related = append([]skillRelatedHint(nil), selection.Related...)
		out = append(out, selection)
	}
	for i := range out {
		out[i].Related = dedupeSkillRelatedHints(out[i].Related)
	}
	return out
}

// Dedupe keeps the schema single-relation while making LLM relation discovery
// tolerant of repeated targets after candidate-level aggregation.
func dedupeKnowledgeRelatedHints(hints []knowledgeRelatedHint) []knowledgeRelatedHint {
	out := make([]knowledgeRelatedHint, 0, len(hints))
	positions := map[string]int{}
	for _, hint := range hints {
		if idx, ok := positions[hint.FactID]; ok {
			if knowledgeRelationPriority(hint.Relation) > knowledgeRelationPriority(out[idx].Relation) {
				out[idx].Relation = hint.Relation
			}
			continue
		}
		positions[hint.FactID] = len(out)
		out = append(out, hint)
	}
	return out
}

func dedupeSkillRelatedHints(hints []skillRelatedHint) []skillRelatedHint {
	out := make([]skillRelatedHint, 0, len(hints))
	positions := map[string]int{}
	for _, hint := range hints {
		if idx, ok := positions[hint.SkillID]; ok {
			if skillRelationPriority(hint.Relation) > skillRelationPriority(out[idx].Relation) {
				out[idx].Relation = hint.Relation
			}
			continue
		}
		positions[hint.SkillID] = len(out)
		out = append(out, hint)
	}
	return out
}

func knowledgeRelationPriority(relation knowledgeRelationKind) int {
	switch relation {
	case knowledgeRelationConflict:
		return 6
	case knowledgeRelationEquivalent:
		return 5
	case knowledgeRelationSupersedes:
		return 4
	case knowledgeRelationSameEntitySlot:
		return 3
	case knowledgeRelationDependsOn:
		return 2
	case knowledgeRelationPossiblyAffects:
		return 1
	default:
		return 0
	}
}

func skillRelationPriority(relation skillRelationKind) int {
	switch relation {
	case skillRelationPatchableGap:
		return 6
	case skillRelationStalePredecessor:
		return 5
	case skillRelationSameWorkflow:
		return 4
	case skillRelationOverlappingTrigger:
		return 3
	case skillRelationBroaderWorkflow, skillRelationNarrowerWorkflow:
		return 2
	default:
		return 0
	}
}

func validateKnowledgeRelatedDiscovery(candidates []factCandidate, catalog []factCatalogItem, selections []knowledgeRelatedSelection, limit int) error {
	candidateRefs := make(map[CandidateRef]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateRefs[candidate.Ref] = struct{}{}
	}
	factIDs := make(map[string]struct{}, len(catalog))
	for _, item := range catalog {
		factIDs[item.ID] = struct{}{}
	}
	if limit <= 0 {
		limit = defaultMaxRelatedPerCandidate
	}

	for _, selection := range selections {
		if _, ok := candidateRefs[selection.CandidateRef]; !ok {
			return fmt.Errorf("related discovery: unknown knowledge candidate %q", selection.CandidateRef)
		}
		if len(selection.Related) > limit {
			return fmt.Errorf("related discovery: candidate %q selected %d facts, limit %d", selection.CandidateRef, len(selection.Related), limit)
		}
		seen := map[string]struct{}{}
		for _, hint := range selection.Related {
			if _, ok := factIDs[hint.FactID]; !ok {
				return fmt.Errorf("related discovery: unknown fact id %q", hint.FactID)
			}
			if !validKnowledgeRelation(hint.Relation) {
				return fmt.Errorf("related discovery: invalid knowledge relation %q", hint.Relation)
			}
			if _, ok := seen[hint.FactID]; ok {
				return fmt.Errorf("related discovery: duplicate fact id %q", hint.FactID)
			}
			seen[hint.FactID] = struct{}{}
		}
	}
	return nil
}

func validateSkillRelatedDiscovery(candidates []skillCandidate, catalog []skillCatalogItem, selections []skillRelatedSelection, limit int) error {
	candidateRefs := make(map[CandidateRef]skillCandidate, len(candidates))
	for _, candidate := range candidates {
		candidateRefs[candidate.Ref] = candidate
	}
	skillIDs := make(map[string]skillCatalogItem, len(catalog))
	skillNameToID := make(map[string]string, len(catalog))
	for _, item := range catalog {
		skillIDs[item.ID] = item
		skillNameToID[item.Name] = item.ID
	}
	if limit <= 0 {
		limit = defaultMaxRelatedPerCandidate
	}

	selectedByCandidate := make(map[CandidateRef]map[string]struct{}, len(selections))
	for _, selection := range selections {
		if _, ok := candidateRefs[selection.CandidateRef]; !ok {
			return fmt.Errorf("related discovery: unknown skill candidate %q", selection.CandidateRef)
		}
		if len(selection.Related) > limit {
			return fmt.Errorf("related discovery: candidate %q selected %d skills, limit %d", selection.CandidateRef, len(selection.Related), limit)
		}
		seen := map[string]struct{}{}
		for _, hint := range selection.Related {
			if _, ok := skillIDs[hint.SkillID]; !ok {
				return fmt.Errorf("related discovery: unknown skill id %q", hint.SkillID)
			}
			if !validSkillRelation(hint.Relation) {
				return fmt.Errorf("related discovery: invalid skill relation %q", hint.Relation)
			}
			if _, ok := seen[hint.SkillID]; ok {
				return fmt.Errorf("related discovery: duplicate skill id %q", hint.SkillID)
			}
			seen[hint.SkillID] = struct{}{}
		}
		selectedByCandidate[selection.CandidateRef] = seen
	}

	for ref, candidate := range candidateRefs {
		if candidate.SessionSkillContext == nil {
			continue
		}
		selected := selectedByCandidate[ref]
		for _, usedRef := range candidate.SessionSkillContext.UsedSkillRefs {
			var requiredID string
			if item, ok := skillIDs[usedRef]; ok {
				requiredID = item.ID
			} else if matchedID, matchedByName := skillNameToID[usedRef]; matchedByName {
				requiredID = matchedID
			} else {
				continue
			}
			if _, ok := selected[requiredID]; !ok {
				return fmt.Errorf("related discovery: candidate %q omitted used skill %q", ref, usedRef)
			}
		}
	}
	return nil
}

func validKnowledgeRelation(relation knowledgeRelationKind) bool {
	switch relation {
	case knowledgeRelationEquivalent,
		knowledgeRelationConflict,
		knowledgeRelationSupersedes,
		knowledgeRelationDependsOn,
		knowledgeRelationPossiblyAffects,
		knowledgeRelationSameEntitySlot:
		return true
	default:
		return false
	}
}

func validSkillRelation(relation skillRelationKind) bool {
	switch relation {
	case skillRelationSameWorkflow,
		skillRelationOverlappingTrigger,
		skillRelationBroaderWorkflow,
		skillRelationNarrowerWorkflow,
		skillRelationPatchableGap,
		skillRelationStalePredecessor:
		return true
	default:
		return false
	}
}
