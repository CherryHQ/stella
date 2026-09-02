package reflect

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/skill"
)

func TestBuildFactRelatedBundleSplitsSingletonsAndCatalogsReflectWorldFacts(t *testing.T) {
	ctx := context.Background()
	userID := "user-1"
	agentID := "agent-1"
	mem := memorytest.New()
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	mem.AddFact(userID, agentID, memory.Fact{
		ID:        "profile-1",
		Subject:   memory.FactSubjectUser,
		Content:   "The user prefers concise Chinese replies.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceManual,
		UpdatedAt: now,
	})
	mem.AddFact(userID, agentID, memory.Fact{
		ID:        "soul-1",
		Subject:   memory.FactSubjectAgent,
		Content:   "Default to direct, pragmatic answers.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceReflect,
		UpdatedAt: now,
	})
	mem.AddFact(userID, agentID, memory.Fact{
		ID:        "world-reflect",
		Subject:   memory.FactSubjectWorld,
		Content:   "Reflect generation and reconciliation are separate stages.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceReflect,
		UpdatedAt: now,
	})
	mem.AddFact(userID, agentID, memory.Fact{
		ID:        "world-manual",
		Subject:   memory.FactSubjectWorld,
		Content:   "Manual facts should not be auto-maintained by reflect.",
		Status:    memory.FactStatusActive,
		Source:    memory.SourceManual,
		UpdatedAt: now,
	})
	if _, err := mem.AddConstraint(ctx, userID, agentID, "Never delete files without confirmation."); err != nil {
		t.Fatal(err)
	}

	bundle, err := buildFactRelatedBundle(ctx, mem, mem, userID, agentID, []factCandidate{
		validFactCandidate("fact-0001", factSubjectUser),
		validFactCandidate("fact-0002", factSubjectAgent),
		validFactCandidate("fact-0003", factSubjectWorld),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(bundle.Profile.Candidates) != 1 || bundle.Profile.Current == nil || bundle.Profile.Current.ID != "profile-1" {
		t.Fatalf("unexpected profile bundle: %#v", bundle.Profile)
	}
	if len(bundle.Soul.Candidates) != 1 || bundle.Soul.Current == nil || bundle.Soul.Current.ID != "soul-1" {
		t.Fatalf("unexpected soul bundle: %#v", bundle.Soul)
	}
	if len(bundle.Soul.ActiveConstraints) != 1 {
		t.Fatalf("expected one active constraint, got %#v", bundle.Soul.ActiveConstraints)
	}
	if len(bundle.Knowledge.Candidates) != 1 {
		t.Fatalf("unexpected knowledge candidates: %#v", bundle.Knowledge.Candidates)
	}
	if len(bundle.Knowledge.Catalog) != 1 || bundle.Knowledge.Catalog[0].ID != "world-reflect" {
		t.Fatalf("expected only reflect-owned world fact in catalog, got %#v", bundle.Knowledge.Catalog)
	}
}

func TestBuildSkillRelatedCatalogUsesReflectOwnedStoreView(t *testing.T) {
	ctx := context.Background()
	store := &fakeReflectSkillCatalogStore{
		rows: []skill.Skill{{
			ID:          "skill-1",
			Scope:       "user_agent",
			UserID:      "user-1",
			AgentID:     "agent-1",
			Name:        "reflect-review",
			Description: "Review reflect candidates before writing them.",
			Status:      "active",
			Version:     7,
			UpdatedAt:   time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC),
		}},
	}

	catalog, err := buildSkillRelatedCatalog(ctx, store, "user-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}

	if store.listUserID != "user-1" || store.listAgentID != "agent-1" {
		t.Fatalf("store called with wrong user-agent: user=%q agent=%q", store.listUserID, store.listAgentID)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected one skill catalog item, got %#v", catalog)
	}
	if catalog[0].ID != "skill-1" || catalog[0].Name != "reflect-review" || catalog[0].Version != 7 {
		t.Fatalf("unexpected skill catalog item: %#v", catalog[0])
	}
}

func TestBuildSkillRelatedBundleLoadsDuplicateSkillOnlyOnce(t *testing.T) {
	store := &fakeSkillRelatedBundleStore{
		fakeReflectSkillCatalogStore: fakeReflectSkillCatalogStore{
			rows: []skill.Skill{{ID: "skill-shared", Name: "shared", Scope: "user_agent", Status: "active", ContentDigest: testSkillContentDigest}},
		},
		files: map[string]string{"skill-shared": "# Shared\n"},
	}

	bundle, err := buildSkillRelatedBundle(context.Background(), store, "user-1", "agent-1", []skillCandidate{
		validSkillCandidate("skill-0001"),
		validSkillCandidate("skill-0002"),
	}, []skillRelatedSelection{
		{CandidateRef: "skill-0001", Related: []skillRelatedHint{{SkillID: "skill-shared", Relation: skillRelationSameWorkflow}}},
		{CandidateRef: "skill-0002", Related: []skillRelatedHint{{SkillID: "skill-shared", Relation: skillRelationOverlappingTrigger}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.RelatedRecords) != 1 || len(store.loaded) != 1 || store.loaded[0] != "skill-shared" {
		t.Fatalf("expected one global load for the shared skill, records=%#v loaded=%#v", bundle.RelatedRecords, store.loaded)
	}
}

type fakeReflectSkillCatalogStore struct {
	rows        []skill.Skill
	listUserID  string
	listAgentID string
}

func (s *fakeReflectSkillCatalogStore) ListActiveReflectOwnedUserAgentSkills(_ context.Context, userID string, agentID string) ([]skill.Skill, error) {
	s.listUserID = userID
	s.listAgentID = agentID
	return append([]skill.Skill(nil), s.rows...), nil
}
