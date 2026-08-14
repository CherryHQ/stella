package reflect

import (
	"context"
	"fmt"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/skills"
)

func TestAttachKnowledgeRelatedRecordsUsesSelectedFacts(t *testing.T) {
	bundle := knowledgeRelatedBundle{
		Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
		Catalog: []factCatalogItem{
			{ID: "fact-a", Record: memory.Fact{ID: "fact-a", Content: "First related fact."}},
			{ID: "fact-b", Record: memory.Fact{ID: "fact-b", Content: "Second related fact."}},
		},
		Limits: relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedKnowledgePerCandidate},
	}

	updated, err := attachKnowledgeRelatedRecords(bundle, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related: []knowledgeRelatedHint{
			{FactID: "fact-b", Relation: knowledgeRelationConflict},
			{FactID: "fact-a", Relation: knowledgeRelationEquivalent},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if len(updated.RelatedRecords) != 2 || updated.RelatedRecords[0].ID != "fact-b" || updated.RelatedRecords[1].ID != "fact-a" {
		t.Fatalf("unexpected related records: %#v", updated.RelatedRecords)
	}
	if len(updated.RelationHints) != 1 || updated.RelationHints[0].Related[0].Relation != knowledgeRelationConflict {
		t.Fatalf("expected relation hints to be preserved for reconciliation, got %#v", updated.RelationHints)
	}
}

func TestBuildSkillRelatedBundleLoadsSelectedSkillContent(t *testing.T) {
	store := &fakeSkillRelatedBundleStore{
		fakeReflectSkillCatalogStore: fakeReflectSkillCatalogStore{
			rows: []skills.Skill{
				{ID: "skill-a", Name: "alpha", Scope: "user_agent", Status: "active", Version: 1, ContentDigest: testSkillContentDigest, Metadata: []byte(`{"created_by":"reflect"}`)},
				{ID: "skill-b", Name: "beta", Scope: "user_agent", Status: "active", Version: 2, ContentDigest: testSkillContentDigest, Metadata: []byte(`{"created_by":"reflect"}`)},
			},
		},
		files: map[string]string{
			"skill-a": "# Alpha\n",
			"skill-b": "# Beta\n",
		},
	}

	bundle, err := buildSkillRelatedBundle(context.Background(), store, "user-1", "agent-1", []skillCandidate{
		validSkillCandidate("skill-0001"),
	}, []skillRelatedSelection{{
		CandidateRef: "skill-0001",
		Related:      []skillRelatedHint{{SkillID: "skill-b", Relation: skillRelationPatchableGap}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if len(bundle.RelatedRecords) != 1 || bundle.RelatedRecords[0].Skill.ID != "skill-b" || bundle.RelatedRecords[0].MainFileContent != "# Beta\n" {
		t.Fatalf("unexpected skill related records: %#v", bundle.RelatedRecords)
	}
	if len(bundle.RelationHints) != 1 || bundle.RelationHints[0].Related[0].Relation != skillRelationPatchableGap {
		t.Fatalf("expected skill relation hints to be preserved for reconciliation, got %#v", bundle.RelationHints)
	}
	if len(store.loaded) != 1 || store.loaded[0] != "skill-b" {
		t.Fatalf("expected only selected skill-b to be loaded, got %#v", store.loaded)
	}
}

type fakeSkillRelatedBundleStore struct {
	fakeReflectSkillCatalogStore
	files  map[string]string
	loaded []string
}

func (s *fakeSkillRelatedBundleStore) LoadExactRevision(_ context.Context, identity skills.Skill, digest string) (skills.ManagedRevision, error) {
	if digest == "" || digest != identity.ContentDigest {
		return skills.ManagedRevision{}, fmt.Errorf("unexpected exact digest %q for %q", digest, identity.ID)
	}
	s.loaded = append(s.loaded, identity.ID)
	return skills.ManagedRevision{
		Skill: identity,
		Files: map[string][]byte{skills.MainFile: []byte(s.files[identity.ID])},
	}, nil
}
