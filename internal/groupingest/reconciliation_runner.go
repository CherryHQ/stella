package groupingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

type ReconciliationRunner struct {
	Stream  providers.StreamFunc
	Model   ai.Model
	Options ai.CompleteOptions
}

func (r ReconciliationRunner) Run(
	ctx context.Context,
	unit GroupReviewUnit,
	bundle GroupRelatedBundle,
) (GroupReconciliationPlan, memory.GroupFactPlan, error) {
	var runtimePlan GroupReconciliationPlan
	runner := reflectpkg.StructuredCaptureRunner{Stream: r.Stream, Model: r.Model, Options: r.Options}
	_, err := runner.Run(
		ctx,
		groupFactReconciliationPrompt,
		renderGroupRelatedBundle(bundle),
		reflectpkg.StructuredCaptureProtocol{
			AllowedTools:  reflectpkg.AllowedCaptureTools(toolSubmitGroupReconciliation),
			SubmitName:    toolSubmitGroupReconciliation,
			RepairRetries: true,
			RepairInstructions: []string{
				"Cover every candidate_ref exactly once.",
				"Use only fact_ids present in active_group_facts.",
				"Do not overlap target_fact_ids across operations.",
				"Use noop rather than omitting an intentionally unchanged candidate.",
			},
			PayloadsValidator: func(calls []ai.ToolCall) error {
				decoded, decodeErr := decodeGroupReconciliation(calls)
				if decodeErr != nil {
					return decodeErr
				}
				normalized, validateErr := validateGroupReconciliationPlan(decoded, unit, bundle)
				if validateErr != nil {
					return validateErr
				}
				runtimePlan = normalized
				return nil
			},
		},
		groupFactReconciliationTools(),
	)
	if err != nil {
		return GroupReconciliationPlan{}, memory.GroupFactPlan{}, fmt.Errorf("reconcile group facts: %w", err)
	}
	writePlan, err := groupFactWritePlan(runtimePlan, unit, bundle)
	if err != nil {
		return GroupReconciliationPlan{}, memory.GroupFactPlan{}, err
	}
	return runtimePlan, writePlan, nil
}

func decodeGroupReconciliation(calls []ai.ToolCall) (GroupReconciliationPlan, error) {
	for _, call := range calls {
		if call.Name == toolSubmitGroupReconciliation {
			return reflectpkg.DecodeStructuredCapturePayload[GroupReconciliationPlan](call)
		}
	}
	return GroupReconciliationPlan{}, fmt.Errorf("missing %s", toolSubmitGroupReconciliation)
}

func validateGroupReconciliationPlan(
	plan GroupReconciliationPlan,
	unit GroupReviewUnit,
	bundle GroupRelatedBundle,
) (GroupReconciliationPlan, error) {
	if len(plan.Operations) > len(bundle.Candidates) {
		return GroupReconciliationPlan{}, fmt.Errorf("operations exceed accepted candidate count")
	}
	candidates := make(map[reflectpkg.CandidateRef]GroupFactCandidate, len(bundle.Candidates))
	for _, candidate := range bundle.Candidates {
		candidates[candidate.Ref] = candidate
	}
	covered := make(map[reflectpkg.CandidateRef]struct{}, len(candidates))
	targeted := make(map[string]struct{})

	for i := range plan.Operations {
		op := &plan.Operations[i]
		op.NewContent = strings.TrimSpace(op.NewContent)
		op.Rationale = strings.TrimSpace(op.Rationale)
		if op.Rationale == "" {
			return GroupReconciliationPlan{}, fmt.Errorf("operation %d requires rationale", i)
		}
		if len(op.CandidateRefs) == 0 {
			return GroupReconciliationPlan{}, fmt.Errorf("operation %d requires candidate_refs", i)
		}
		var opSubject memory.GroupFactSubject
		var opSubjectID string
		for _, ref := range op.CandidateRefs {
			candidate, ok := candidates[ref]
			if !ok {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d has unknown candidate_ref %q", i, ref)
			}
			if _, exists := covered[ref]; exists {
				return GroupReconciliationPlan{}, fmt.Errorf("candidate_ref %q is covered more than once", ref)
			}
			covered[ref] = struct{}{}
			subject, subjectID, err := resolvedCandidateSubject(candidate, unit)
			if err != nil {
				return GroupReconciliationPlan{}, err
			}
			if opSubject == "" {
				opSubject, opSubjectID = subject, subjectID
			} else if opSubject != subject || opSubjectID != subjectID {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d combines different typed subjects", i)
			}
		}

		for targetIndex, targetID := range op.TargetFactIDs {
			targetID = strings.TrimSpace(targetID)
			fact, ok := bundle.factsByID[targetID]
			if !ok {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d targets unknown fact %q", i, targetID)
			}
			if fact.Subject != opSubject || fact.SubjectID != opSubjectID {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d target %q has a different typed subject", i, targetID)
			}
			if _, exists := targeted[targetID]; exists {
				return GroupReconciliationPlan{}, fmt.Errorf("target fact %q appears more than once", targetID)
			}
			targeted[targetID] = struct{}{}
			op.TargetFactIDs[targetIndex] = targetID
		}
		if len(op.TargetFactIDs) > maxGroupFactTargetsPerOperation {
			return GroupReconciliationPlan{}, fmt.Errorf("operation %d has more than %d targets", i, maxGroupFactTargetsPerOperation)
		}

		switch op.Operation {
		case memory.GroupFactActionNoop:
			if len(op.TargetFactIDs) != 0 || op.NewContent != "" {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d noop cannot mutate facts", i)
			}
		case memory.GroupFactActionCreate:
			if len(op.TargetFactIDs) != 0 || op.NewContent == "" {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d create requires new_content and no targets", i)
			}
			if hasExactActiveGroupFact(bundle, opSubject, opSubjectID, op.NewContent) {
				op.Operation = memory.GroupFactActionNoop
				op.NewContent = ""
			}
		case memory.GroupFactActionReplaceMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent == "" {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d replace_many requires targets and new_content", i)
			}
			if len(op.TargetFactIDs) == 1 &&
				strings.TrimSpace(bundle.factsByID[op.TargetFactIDs[0]].Content) == op.NewContent {
				op.Operation = memory.GroupFactActionNoop
				op.TargetFactIDs = nil
				op.NewContent = ""
			}
		case memory.GroupFactActionDeprecateMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent != "" {
				return GroupReconciliationPlan{}, fmt.Errorf("operation %d deprecate_many requires targets and no new_content", i)
			}
		default:
			return GroupReconciliationPlan{}, fmt.Errorf("operation %d has unsupported action %q", i, op.Operation)
		}
		if op.NewContent != "" && reflectpkg.ContainsSecretLikeContent(op.NewContent) {
			return GroupReconciliationPlan{}, fmt.Errorf("operation %d new_content contains secret-like data", i)
		}
	}
	for ref := range candidates {
		if _, ok := covered[ref]; !ok {
			return GroupReconciliationPlan{}, fmt.Errorf("candidate_ref %q is not covered", ref)
		}
	}
	return plan, nil
}

func groupFactWritePlan(
	plan GroupReconciliationPlan,
	unit GroupReviewUnit,
	bundle GroupRelatedBundle,
) (memory.GroupFactPlan, error) {
	candidates := make(map[reflectpkg.CandidateRef]GroupFactCandidate, len(bundle.Candidates))
	for _, candidate := range bundle.Candidates {
		candidates[candidate.Ref] = candidate
	}
	writePlan := memory.GroupFactPlan{Operations: make([]memory.GroupFactOperation, 0, len(plan.Operations))}
	for _, op := range plan.Operations {
		candidate := candidates[op.CandidateRefs[0]]
		subject, subjectID, err := resolvedCandidateSubject(candidate, unit)
		if err != nil {
			return memory.GroupFactPlan{}, err
		}
		writePlan.Operations = append(writePlan.Operations, memory.GroupFactOperation{
			Action:        op.Operation,
			Subject:       subject,
			SubjectID:     subjectID,
			TargetFactIDs: append([]string(nil), op.TargetFactIDs...),
			NewContent:    op.NewContent,
		})
	}
	return writePlan, nil
}

func resolvedCandidateSubject(
	candidate GroupFactCandidate,
	unit GroupReviewUnit,
) (memory.GroupFactSubject, string, error) {
	if candidate.Subject == memory.GroupFactSubjectGroup {
		return candidate.Subject, "", nil
	}
	entry, ok := unit.Subjects[candidate.SubjectRef]
	if !ok || entry.Subject != candidate.Subject {
		return "", "", fmt.Errorf("candidate %q has invalid subject_ref %q", candidate.Ref, candidate.SubjectRef)
	}
	return candidate.Subject, entry.SubjectID, nil
}

func hasExactActiveGroupFact(
	bundle GroupRelatedBundle,
	subject memory.GroupFactSubject,
	subjectID string,
	content string,
) bool {
	content = strings.TrimSpace(content)
	for _, fact := range bundle.factsByID {
		if fact.Subject == subject && fact.SubjectID == subjectID && strings.TrimSpace(fact.Content) == content {
			return true
		}
	}
	return false
}

func renderGroupRelatedBundle(bundle GroupRelatedBundle) string {
	payload := struct {
		ReviewText string               `json:"review_context"`
		Candidates []GroupFactCandidate `json:"candidates"`
		Facts      []GroupRelatedFact   `json:"active_group_facts"`
	}{
		ReviewText: bundle.ReviewText,
		Candidates: bundle.Candidates,
		Facts:      bundle.Facts,
	}
	data, _ := json.Marshal(payload)
	return "<group_reconciliation_bundle>\n" + string(data) + "\n</group_reconciliation_bundle>\n"
}
