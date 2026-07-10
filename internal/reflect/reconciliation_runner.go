package reflect

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

func (r candidateLineReviewer) discoverKnowledgeRelations(ctx context.Context, bundle knowledgeRelatedBundle) ([]knowledgeRelatedSelection, error) {
	if len(bundle.Candidates) == 0 || len(bundle.Catalog) == 0 {
		return nil, nil
	}
	var selections []knowledgeRelatedSelection
	_, err := r.capture(ctx, knowledgeRelatedDiscoveryPrompt, renderCaptureInput(bundle), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitKnowledgeRelatedDiscovery),
		SubmitName:   toolSubmitKnowledgeRelatedDiscovery,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			selections, err = decodeKnowledgeRelatedDiscoveryCall(calls)
			if err != nil {
				return err
			}
			selections = normalizeKnowledgeRelatedSelections(selections)
			return validateKnowledgeRelatedDiscovery(bundle.Candidates, bundle.Catalog, selections, bundle.Limits.MaxRelatedPerCandidate)
		},
	}, knowledgeRelatedDiscoveryTools())
	if err != nil {
		return nil, fmt.Errorf("discover knowledge relations: %w", err)
	}
	return selections, nil
}

func (r candidateLineReviewer) discoverSkillRelations(ctx context.Context, candidates []skillCandidate, catalog []skillCatalogItem) ([]skillRelatedSelection, error) {
	if len(candidates) == 0 || len(catalog) == 0 {
		return nil, nil
	}
	input := struct {
		Candidates []skillCandidate    `json:"candidates"`
		Catalog    []skillCatalogItem  `json:"catalog"`
		Limits     relatedBundleLimits `json:"limits"`
	}{
		Candidates: candidates,
		Catalog:    catalog,
		Limits:     relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedPerCandidate},
	}
	var selections []skillRelatedSelection
	_, err := r.capture(ctx, skillRelatedDiscoveryPrompt, renderCaptureInput(input), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillRelatedDiscovery),
		SubmitName:   toolSubmitSkillRelatedDiscovery,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			selections, err = decodeSkillRelatedDiscoveryCall(calls)
			if err != nil {
				return err
			}
			selections = normalizeSkillRelatedSelections(selections)
			return validateSkillRelatedDiscovery(candidates, catalog, selections, defaultMaxRelatedPerCandidate)
		},
	}, skillRelatedDiscoveryTools())
	if err != nil {
		return nil, fmt.Errorf("discover skill relations: %w", err)
	}
	return selections, nil
}

func (r candidateLineReviewer) reconcileFacts(ctx context.Context, bundle factRelatedBundle) (factReconciliationPlan, error) {
	var plan factReconciliationPlan
	_, err := r.capture(ctx, factReconciliationPrompt, renderCaptureInput(bundle), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitFactReconciliation),
		SubmitName:   toolSubmitFactReconciliation,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			plan, err = decodeFactReconciliationCall(calls)
			if err != nil {
				return err
			}
			plan = normalizeFactReconciliationPlan(bundle, plan)
			return validateFactReconciliationPlan(bundle, plan)
		},
	}, factReconciliationTools())
	if err != nil {
		return factReconciliationPlan{}, fmt.Errorf("reconcile facts: %w", err)
	}
	return plan, nil
}

func (r candidateLineReviewer) reconcileSkills(ctx context.Context, bundle skillRelatedBundle) (skillReconciliationPlan, error) {
	var plan skillReconciliationPlan
	_, err := r.capture(ctx, skillReconciliationPrompt, renderCaptureInput(bundle), captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillReconciliation),
		SubmitName:   toolSubmitSkillReconciliation,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var err error
			plan, err = decodeSkillReconciliationCall(calls)
			if err != nil {
				return err
			}
			plan = normalizeSkillReconciliationPlan(plan)
			return validateSkillReconciliationPlan(bundle, plan)
		},
	}, skillReconciliationTools())
	if err != nil {
		return skillReconciliationPlan{}, fmt.Errorf("reconcile skills: %w", err)
	}
	return plan, nil
}

func normalizeFactReconciliationPlan(bundle factRelatedBundle, plan factReconciliationPlan) factReconciliationPlan {
	plan.Profile.CandidateRefs, plan.Profile.CoveredCandidateRefs = normalizeCandidateRefLists(plan.Profile.CandidateRefs, plan.Profile.CoveredCandidateRefs)
	plan.Soul.CandidateRefs, plan.Soul.CoveredCandidateRefs = normalizeCandidateRefLists(plan.Soul.CandidateRefs, plan.Soul.CoveredCandidateRefs)
	plan.Profile.ProposedContent = normalizeEquivalentSingletonNoopContent(plan.Profile.Operation, plan.Profile.ProposedContent, bundle.Profile.Current)
	plan.Soul.ProposedContent = normalizeEquivalentSingletonNoopContent(plan.Soul.Operation, plan.Soul.ProposedContent, bundle.Soul.Current)
	for i := range plan.Knowledge.Operations {
		op := &plan.Knowledge.Operations[i]
		op.CandidateRefs, op.CoveredCandidateRefs = normalizeCandidateRefLists(op.CandidateRefs, op.CoveredCandidateRefs)
	}
	return plan
}

func normalizeEquivalentSingletonNoopContent(operation singletonFactOperation, proposed string, current *memory.Fact) string {
	if operation != singletonOperationNoop || current == nil {
		return proposed
	}
	if sameFactContent(proposed, current.Content) {
		return ""
	}
	return proposed
}

func normalizeSkillReconciliationPlan(plan skillReconciliationPlan) skillReconciliationPlan {
	for i := range plan.Operations {
		op := &plan.Operations[i]
		op.CandidateRefs, op.CoveredCandidateRefs = normalizeCandidateRefLists(op.CandidateRefs, op.CoveredCandidateRefs)
	}
	return plan
}

// normalizeCandidateRefLists removes duplicates within each list while keeping
// cross-list overlap visible to validation and protocol repair.
func normalizeCandidateRefLists(direct []CandidateRef, covered []CandidateRef) ([]CandidateRef, []CandidateRef) {
	return uniqueCandidateRefs(direct), uniqueCandidateRefs(covered)
}

func uniqueCandidateRefs(refs []CandidateRef) []CandidateRef {
	out := make([]CandidateRef, 0, len(refs))
	seen := map[CandidateRef]struct{}{}
	for _, ref := range refs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func renderCaptureInput(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
