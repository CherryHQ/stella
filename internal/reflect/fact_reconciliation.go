package reflect

import (
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
)

type singletonFactOperation string

const (
	singletonOperationNoop    singletonFactOperation = "noop"
	singletonOperationCreate  singletonFactOperation = "create_singleton"
	singletonOperationReplace singletonFactOperation = "replace_singleton"
)

type knowledgeFactOperation string

const (
	knowledgeOperationNoop          knowledgeFactOperation = "noop"
	knowledgeOperationCreate        knowledgeFactOperation = "create"
	knowledgeOperationReplaceMany   knowledgeFactOperation = "replace_many"
	knowledgeOperationDeprecateMany knowledgeFactOperation = "deprecate_many"
)

type factReconciliationPlan struct {
	Profile   factSingletonWritePlan
	Soul      soulSingletonWritePlan
	Knowledge knowledgeWritePlan
}

type factSingletonWritePlan struct {
	Operation            singletonFactOperation `json:"operation"`
	CandidateRefs        []CandidateRef         `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs []CandidateRef         `json:"covered_candidate_refs,omitempty"`
	ProposedContent      string                 `json:"proposed_content,omitempty"`
	Rationale            string                 `json:"rationale,omitempty"`
}

type soulSingletonWritePlan struct {
	Operation               singletonFactOperation `json:"operation"`
	CandidateRefs           []CandidateRef         `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs    []CandidateRef         `json:"covered_candidate_refs,omitempty"`
	ProposedContent         string                 `json:"proposed_content,omitempty"`
	Rationale               string                 `json:"rationale,omitempty"`
	ConstraintConflictNotes []string               `json:"constraint_conflict_notes,omitempty"`
}

type knowledgeWritePlan struct {
	Operations []knowledgeWriteOperation `json:"operations,omitempty"`
}

type knowledgeWriteOperation struct {
	Operation            knowledgeFactOperation `json:"operation"`
	CandidateRefs        []CandidateRef         `json:"candidate_refs,omitempty"`
	CoveredCandidateRefs []CandidateRef         `json:"covered_candidate_refs,omitempty"`
	TargetFactIDs        []string               `json:"target_fact_ids,omitempty"`
	NewContent           string                 `json:"new_content,omitempty"`
	Rationale            string                 `json:"rationale,omitempty"`
}

func validateFactReconciliationPlan(bundle factRelatedBundle, plan factReconciliationPlan) error {
	coverage := newCandidateCoverage("fact", allFactCandidateRefs(bundle))
	if err := validateFactSingletonPlan("profile", bundle.Profile.Candidates, bundle.Profile.Current, plan.Profile, coverage); err != nil {
		return err
	}
	if err := validateSoulSingletonPlan(bundle.Soul.Candidates, bundle.Soul.Current, plan.Soul, coverage); err != nil {
		return err
	}
	if err := validateKnowledgePlan(bundle.Knowledge, plan.Knowledge, coverage); err != nil {
		return err
	}
	if err := coverage.requireComplete(); err != nil {
		return err
	}
	return nil
}

func validateFactSingletonPlan(part string, candidates []factCandidate, current *memory.Fact, plan factSingletonWritePlan, coverage *candidateCoverage) error {
	if !validSingletonOperation(plan.Operation) {
		return fmt.Errorf("fact reconciliation: invalid %s operation %q", part, plan.Operation)
	}
	if err := coverage.markAll(part, factRefsFromCandidates(candidates), plan.CandidateRefs, plan.CoveredCandidateRefs); err != nil {
		return err
	}
	switch plan.Operation {
	case singletonOperationNoop:
		if plan.ProposedContent != "" {
			return fmt.Errorf("fact reconciliation: %s noop cannot propose content", part)
		}
	case singletonOperationCreate:
		if current != nil {
			return fmt.Errorf("fact reconciliation: %s create requires empty singleton", part)
		}
		if plan.ProposedContent == "" {
			return fmt.Errorf("fact reconciliation: %s create requires proposed content", part)
		}
	case singletonOperationReplace:
		if current == nil {
			return fmt.Errorf("fact reconciliation: %s replace requires current singleton", part)
		}
		if plan.ProposedContent == "" {
			return fmt.Errorf("fact reconciliation: %s replace requires proposed content", part)
		}
		if sameFactContent(plan.ProposedContent, current.Content) {
			return fmt.Errorf("fact reconciliation: %s replace has no material change; use noop", part)
		}
	}
	return nil
}

func validateSoulSingletonPlan(candidates []factCandidate, current *memory.Fact, plan soulSingletonWritePlan, coverage *candidateCoverage) error {
	if len(plan.ConstraintConflictNotes) > 0 && plan.Operation != singletonOperationNoop {
		return fmt.Errorf("fact reconciliation: soul write conflicts with active constraints")
	}
	return validateFactSingletonPlan("soul", candidates, current, factSingletonWritePlan{
		Operation:            plan.Operation,
		CandidateRefs:        plan.CandidateRefs,
		CoveredCandidateRefs: plan.CoveredCandidateRefs,
		ProposedContent:      plan.ProposedContent,
		Rationale:            plan.Rationale,
	}, coverage)
}

func validateKnowledgePlan(bundle knowledgeRelatedBundle, plan knowledgeWritePlan, coverage *candidateCoverage) error {
	related := make(map[string]memory.Fact, len(bundle.RelatedRecords))
	for _, fact := range bundle.RelatedRecords {
		related[fact.ID] = fact
	}
	allowedRefs := factRefsFromCandidates(bundle.Candidates)
	limit := bundle.Limits.MaxRelatedPerCandidate
	if limit <= 0 {
		limit = defaultMaxRelatedPerCandidate
	}

	for _, op := range plan.Operations {
		if !validKnowledgeOperation(op.Operation) {
			return fmt.Errorf("fact reconciliation: invalid knowledge operation %q", op.Operation)
		}
		if err := coverage.markAll("knowledge", allowedRefs, op.CandidateRefs, op.CoveredCandidateRefs); err != nil {
			return err
		}
		if len(op.TargetFactIDs) > limit {
			return fmt.Errorf("fact reconciliation: knowledge target count %d exceeds limit %d", len(op.TargetFactIDs), limit)
		}
		for _, id := range op.TargetFactIDs {
			fact, ok := related[id]
			if !ok {
				return fmt.Errorf("fact reconciliation: target fact %q is not in related bundle", id)
			}
			if fact.Subject != memory.FactSubjectWorld || fact.Status != memory.FactStatusActive || fact.Source != memory.SourceReflect {
				return fmt.Errorf("fact reconciliation: target fact %q is not active reflect-owned world fact", id)
			}
		}
		switch op.Operation {
		case knowledgeOperationNoop:
			if len(op.TargetFactIDs) > 0 || op.NewContent != "" {
				return fmt.Errorf("fact reconciliation: knowledge noop cannot write")
			}
		case knowledgeOperationCreate:
			if len(op.TargetFactIDs) > 0 {
				return fmt.Errorf("fact reconciliation: knowledge create cannot target old facts")
			}
			if op.NewContent == "" {
				return fmt.Errorf("fact reconciliation: knowledge create requires content")
			}
		case knowledgeOperationReplaceMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent == "" {
				return fmt.Errorf("fact reconciliation: knowledge replace_many requires targets and content")
			}
		case knowledgeOperationDeprecateMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent != "" {
				return fmt.Errorf("fact reconciliation: knowledge deprecate_many requires targets only")
			}
		}
	}
	return nil
}

func validSingletonOperation(op singletonFactOperation) bool {
	switch op {
	case singletonOperationNoop, singletonOperationCreate, singletonOperationReplace:
		return true
	default:
		return false
	}
}

func sameFactContent(left string, right string) bool {
	return strings.TrimSpace(left) == strings.TrimSpace(right)
}

func validKnowledgeOperation(op knowledgeFactOperation) bool {
	switch op {
	case knowledgeOperationNoop,
		knowledgeOperationCreate,
		knowledgeOperationReplaceMany,
		knowledgeOperationDeprecateMany:
		return true
	default:
		return false
	}
}

func allFactCandidateRefs(bundle factRelatedBundle) []CandidateRef {
	refs := make([]CandidateRef, 0, len(bundle.Profile.Candidates)+len(bundle.Soul.Candidates)+len(bundle.Knowledge.Candidates))
	refs = append(refs, factRefsFromCandidates(bundle.Profile.Candidates)...)
	refs = append(refs, factRefsFromCandidates(bundle.Soul.Candidates)...)
	refs = append(refs, factRefsFromCandidates(bundle.Knowledge.Candidates)...)
	return refs
}

func factRefsFromCandidates(candidates []factCandidate) []CandidateRef {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	return refs
}

type candidateCoverage struct {
	label string
	all   map[CandidateRef]struct{}
	seen  map[CandidateRef]string
}

func newCandidateCoverage(label string, refs []CandidateRef) *candidateCoverage {
	all := make(map[CandidateRef]struct{}, len(refs))
	for _, ref := range refs {
		all[ref] = struct{}{}
	}
	return &candidateCoverage{
		label: label,
		all:   all,
		seen:  map[CandidateRef]string{},
	}
}

func (c *candidateCoverage) markAll(part string, allowed []CandidateRef, direct []CandidateRef, covered []CandidateRef) error {
	allowedSet := make(map[CandidateRef]struct{}, len(allowed))
	for _, ref := range allowed {
		allowedSet[ref] = struct{}{}
	}

	directSet := make(map[CandidateRef]struct{}, len(direct))
	for _, ref := range direct {
		directSet[ref] = struct{}{}
	}
	for _, ref := range covered {
		if _, ok := directSet[ref]; ok {
			return fmt.Errorf("%s reconciliation: candidate %q appears in both candidate_refs and covered_candidate_refs for %s", c.label, ref, part)
		}
	}

	for _, ref := range append(append([]CandidateRef{}, direct...), covered...) {
		if _, ok := allowedSet[ref]; !ok {
			return fmt.Errorf("%s reconciliation: candidate %q is not allowed in %s plan", c.label, ref, part)
		}
		if previous, ok := c.seen[ref]; ok {
			return fmt.Errorf("%s reconciliation: candidate %q covered by both %s and %s", c.label, ref, previous, part)
		}
		c.seen[ref] = part
	}
	return nil
}

func (c *candidateCoverage) requireComplete() error {
	for ref := range c.all {
		if _, ok := c.seen[ref]; !ok {
			return fmt.Errorf("%s reconciliation: candidate %q is not covered", c.label, ref)
		}
	}
	return nil
}
