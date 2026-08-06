package groupingest

import (
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
)

type GroupRelatedFact struct {
	FactID     string                  `json:"fact_id"`
	Subject    memory.GroupFactSubject `json:"subject"`
	SubjectRef string                  `json:"subject_ref,omitempty"`
	Content    string                  `json:"content"`
}

type GroupRelatedBundle struct {
	ReviewText string               `json:"review_context"`
	Candidates []GroupFactCandidate `json:"candidates"`
	Facts      []GroupRelatedFact   `json:"active_group_facts"`

	factsByID map[string]memory.GroupFact
}

type GroupReconciliationOperation struct {
	Operation     memory.GroupFactAction    `json:"operation"`
	CandidateRefs []reflectpkg.CandidateRef `json:"candidate_refs"`
	TargetFactIDs []string                  `json:"target_fact_ids"`
	NewContent    string                    `json:"new_content,omitempty"`
	Rationale     string                    `json:"rationale"`
}

type GroupReconciliationPlan struct {
	Operations []GroupReconciliationOperation `json:"operations"`
}

func BuildGroupRelatedBundle(
	unit GroupReviewUnit,
	candidates []GroupFactCandidate,
	facts []memory.GroupFact,
) (GroupRelatedBundle, error) {
	bundle := GroupRelatedBundle{
		ReviewText: unit.Text,
		Candidates: append([]GroupFactCandidate(nil), candidates...),
		Facts:      make([]GroupRelatedFact, 0, len(facts)),
		factsByID:  make(map[string]memory.GroupFact, len(facts)),
	}
	subjectRefs := make(map[string]string, len(unit.Subjects))
	for ref, subject := range unit.Subjects {
		subjectRefs[groupActorKey(subject.Subject, subject.SubjectID)] = ref
	}
	storedRef := 1
	for _, fact := range facts {
		if fact.GroupID != unit.GroupID || fact.Status != memory.GroupFactStatusActive {
			return GroupRelatedBundle{}, fmt.Errorf("related fact %s is outside the active current-group scope", fact.ID)
		}
		if fact.Source != memory.GroupFactSourceReflect {
			return GroupRelatedBundle{}, fmt.Errorf("related fact %s is not Reflect-owned", fact.ID)
		}
		subjectRef := ""
		if fact.Subject != memory.GroupFactSubjectGroup {
			subjectRef = subjectRefs[groupActorKey(fact.Subject, fact.SubjectID)]
			if subjectRef == "" {
				subjectRef = fmt.Sprintf("stored-subject-%04d", storedRef)
				storedRef++
			}
		}
		bundle.Facts = append(bundle.Facts, GroupRelatedFact{
			FactID:     fact.ID,
			Subject:    fact.Subject,
			SubjectRef: subjectRef,
			Content:    fact.Content,
		})
		bundle.factsByID[fact.ID] = fact
	}
	return bundle, nil
}
