package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// stubSkillAuthorizer is the test double for the Skill write authorizer: it
// records calls and returns a fixed error so a denial can be asserted to block
// the store write.
type stubSkillAuthorizer struct {
	err   error
	calls int
}

func (s *stubSkillAuthorizer) AuthorizeWorkerWrite(_ context.Context, _, _, _ string, _ bool) error {
	s.calls++
	return s.err
}

func TestExecuteSkillReconciliationPlanWritesCreateAndPatch(t *testing.T) {
	writer := &fakeReflectSkillWriter{}
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{validSkillCandidate("skill-0001"), validSkillCandidate("skill-0002")},
		RelatedRecords: []skillRelatedRecord{{
			ContentDigest: strings.Repeat("a", 64),
			Skill: pkgplugins.Skill{
				ID:            "old-skill",
				Scope:         "user_agent",
				Status:        "active",
				ContentDigest: strings.Repeat("a", 64),
				Version:       4,
				Metadata:      []byte(`{"created_by":"reflect"}`),
			},
			MainFileContent: "# Old skill\n",
		}},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0001"},
			Name:            "new-reflect-skill",
			Description:     "Create a reflect-maintained skill.",
			MainFileContent: "# New skill\n",
		},
		{
			Operation:             skillOperationPatch,
			CandidateRefs:         []CandidateRef{"skill-0002"},
			TargetSkillID:         "old-skill",
			ExpectedContentDigest: strings.Repeat("a", 64),
			Description:           "Patch a reflect-maintained skill.",
			MainFileContent:       "# Patched skill\n",
		},
	}}

	provenance := skillProvenanceInput{
		Context: testReflectProvenanceContext(),
		Decisions: []skillCandidateDecision{
			testSkillCandidateDecision(bundle.Candidates[0], 0.91),
			testSkillCandidateDecision(bundle.Candidates[1], 0.92),
		},
	}
	if _, err := executeSkillReconciliationPlan(context.Background(), writer, &stubSkillAuthorizer{}, "user-1", "agent-1", bundle, plan, provenance); err != nil {
		t.Fatalf("executeSkillReconciliationPlan: %v", err)
	}

	if len(writer.creates) != 1 || writer.creates[0].Name != "new-reflect-skill" {
		t.Fatalf("unexpected creates: %#v", writer.creates)
	}
	if len(writer.creates[0].ChangelogMetadata) == 0 {
		t.Fatal("create is missing changelog provenance")
	}
	var createMetadata reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(writer.creates[0].ChangelogMetadata, &createMetadata); err != nil {
		t.Fatalf("decode create provenance: %v", err)
	}
	if createMetadata.ReflectProvenance.OperationRef != "skill-0001" {
		t.Fatalf("create operation ref = %q", createMetadata.ReflectProvenance.OperationRef)
	}

	// A denied authorization (custom deny / revoked agent grant) blocks the write
	// before it reaches the store, and a nil authorizer fails closed.
	denyWriter := &fakeReflectSkillWriter{}
	denied := &stubSkillAuthorizer{err: errors.New("forbidden")}
	if _, err := executeSkillReconciliationPlan(context.Background(), denyWriter, denied, "user-1", "agent-1", bundle, plan, provenance); err == nil {
		t.Fatal("expected authorization denial to block the write")
	}
	if denied.calls == 0 {
		t.Fatal("authorizer was not consulted before the write")
	}
	if len(denyWriter.creates) != 0 || len(denyWriter.patches) != 0 {
		t.Fatalf("writer must not be called on denial: creates=%#v patches=%#v", denyWriter.creates, denyWriter.patches)
	}
	if _, err := executeSkillReconciliationPlan(context.Background(), denyWriter, nil, "user-1", "agent-1", bundle, plan, provenance); err == nil {
		t.Fatal("expected nil authorizer to fail closed")
	}
	if writer.creates[0].UserID != "user-1" || writer.creates[0].AgentID != "agent-1" {
		t.Fatalf("wrong create owner: %#v", writer.creates[0])
	}
	if len(writer.patches) != 1 || writer.patches[0].ID != "old-skill" || writer.patches[0].ExpectedDigest != strings.Repeat("a", 64) || writer.patches[0].ExpectedVersion != 0 {
		t.Fatalf("unexpected patches: %#v", writer.patches)
	}
	if writer.patches[0].Description == nil || *writer.patches[0].Description != "Patch a reflect-maintained skill." {
		t.Fatalf("patch description not mapped: %#v", writer.patches[0])
	}
	if writer.patches[0].MainFileContent == nil || *writer.patches[0].MainFileContent != "# Patched skill\n" {
		t.Fatalf("patch SKILL.md not mapped: %#v", writer.patches[0])
	}
	if len(writer.patches[0].ChangelogMetadata) == 0 {
		t.Fatal("patch is missing changelog provenance")
	}
	var patchMetadata reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(writer.patches[0].ChangelogMetadata, &patchMetadata); err != nil {
		t.Fatalf("decode patch provenance: %v", err)
	}
	if patchMetadata.ReflectProvenance.OperationRef != "skill-0002" ||
		patchMetadata.ReflectProvenance.RunID != createMetadata.ReflectProvenance.RunID {
		t.Fatalf("unexpected patch provenance header: %#v", patchMetadata.ReflectProvenance.reflectProvenanceHeader)
	}
}

func TestExecuteSkillReconciliationPlanRejectsInvalidPlanBeforeWriting(t *testing.T) {
	writer := &fakeReflectSkillWriter{}
	bundle := skillRelatedBundle{Candidates: []skillCandidate{validSkillCandidate("skill-0001")}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:            skillOperationPatch,
		CandidateRefs:        []CandidateRef{"skill-0001"},
		TargetSkillID:        "missing-skill",
		ExpectedSkillVersion: 1,
		MainFileContent:      "# Invalid\n",
	}}}

	if _, err := executeSkillReconciliationPlan(
		context.Background(),
		writer,
		&stubSkillAuthorizer{},
		"user-1",
		"agent-1",
		bundle,
		plan,
		skillProvenanceInput{Context: testReflectProvenanceContext()},
	); err == nil {
		t.Fatal("expected invalid plan error")
	}
	if len(writer.creates) != 0 || len(writer.patches) != 0 {
		t.Fatalf("writer should not be called, got creates=%#v patches=%#v", writer.creates, writer.patches)
	}
}

func TestExecuteSkillReconciliationPlanNoopDoesNotPersistProvenance(t *testing.T) {
	writer := &fakeReflectSkillWriter{}
	candidate := validSkillCandidate("skill-0001")
	bundle := skillRelatedBundle{Candidates: []skillCandidate{candidate}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:     skillOperationNoop,
		CandidateRefs: []CandidateRef{candidate.Ref},
		Rationale:     "Already represented.",
	}}}

	if _, err := executeSkillReconciliationPlan(
		context.Background(),
		writer,
		&stubSkillAuthorizer{},
		"user-1",
		"agent-1",
		bundle,
		plan,
		skillProvenanceInput{Decisions: []skillCandidateDecision{testSkillCandidateDecision(candidate, 0.9)}},
	); err != nil {
		t.Fatalf("execute noop skill plan: %v", err)
	}
	if len(writer.creates) != 0 || len(writer.patches) != 0 {
		t.Fatalf("noop skill plan persisted writes: creates=%#v patches=%#v", writer.creates, writer.patches)
	}
}

func TestExecuteSkillReconciliationPlanPrebuildsAllProvenanceBeforeWriting(t *testing.T) {
	writer := &fakeReflectSkillWriter{}
	first := validSkillCandidate("skill-0001")
	second := validSkillCandidate("skill-0002")
	second.Learning.Summary = strings.Repeat("x", maxReflectProvenanceBytes)
	bundle := skillRelatedBundle{Candidates: []skillCandidate{first, second}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{first.Ref},
			Name:            "would-otherwise-commit",
			Description:     "first operation",
			MainFileContent: "# First\n",
		},
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{second.Ref},
			Name:            "oversize-provenance",
			Description:     "second operation",
			MainFileContent: "# Second\n",
		},
	}}

	_, err := executeSkillReconciliationPlan(
		context.Background(),
		writer,
		&stubSkillAuthorizer{},
		"user-1",
		"agent-1",
		bundle,
		plan,
		skillProvenanceInput{
			Context: testReflectProvenanceContext(),
			Decisions: []skillCandidateDecision{
				testSkillCandidateDecision(first, 0.91),
				testSkillCandidateDecision(second, 0.92),
			},
		},
	)
	if !errors.Is(err, errReflectProvenanceTooLarge) {
		t.Fatalf("expected oversize provenance error, got %v", err)
	}
	if len(writer.creates) != 0 || len(writer.patches) != 0 {
		t.Fatalf("provenance prebuild failure allowed partial writes: creates=%#v patches=%#v", writer.creates, writer.patches)
	}
}

type fakeReflectSkillWriter struct {
	creates []skills.ReflectSkillCreate
	patches []skills.ReflectSkillPatch
}

func (w *fakeReflectSkillWriter) CreateReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillCreate) (skills.Skill, error) {
	w.creates = append(w.creates, in)
	return skills.Skill{ID: "created-skill", Version: 1}, nil
}

func (w *fakeReflectSkillWriter) PatchReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillPatch) (skills.Skill, error) {
	w.patches = append(w.patches, in)
	return skills.Skill{ID: in.ID, Version: in.ExpectedVersion + 1}, nil
}
