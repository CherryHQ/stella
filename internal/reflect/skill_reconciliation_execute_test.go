package reflect

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// stubSkillAuthorizer is the test double for the ResourceSkill write PEP: it
// records calls and returns a fixed error so a denial (custom deny / revoked
// grant) can be asserted to block the store write.
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
			Skill: pkgplugins.Skill{
				ID:       "old-skill",
				Scope:    "user_agent",
				Status:   "active",
				Version:  4,
				Metadata: []byte(`{"created_by":"reflect"}`),
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
			Operation:            skillOperationPatch,
			CandidateRefs:        []CandidateRef{"skill-0002"},
			TargetSkillID:        "old-skill",
			ExpectedSkillVersion: 4,
			Description:          "Patch a reflect-maintained skill.",
			MainFileContent:      "# Patched skill\n",
		},
	}}

	if _, err := executeSkillReconciliationPlan(context.Background(), writer, &stubSkillAuthorizer{}, "user-1", "agent-1", bundle, plan); err != nil {
		t.Fatalf("executeSkillReconciliationPlan: %v", err)
	}

	if len(writer.creates) != 1 || writer.creates[0].Name != "new-reflect-skill" {
		t.Fatalf("unexpected creates: %#v", writer.creates)
	}

	// A denied authorization (custom deny / revoked agent grant) blocks the write
	// before it reaches the store, and a nil authorizer fails closed.
	denyWriter := &fakeReflectSkillWriter{}
	denied := &stubSkillAuthorizer{err: errors.New("forbidden")}
	if _, err := executeSkillReconciliationPlan(context.Background(), denyWriter, denied, "user-1", "agent-1", bundle, plan); err == nil {
		t.Fatal("expected authorization denial to block the write")
	}
	if denied.calls == 0 {
		t.Fatal("authorizer was not consulted before the write")
	}
	if len(denyWriter.creates) != 0 || len(denyWriter.patches) != 0 {
		t.Fatalf("writer must not be called on denial: creates=%#v patches=%#v", denyWriter.creates, denyWriter.patches)
	}
	if _, err := executeSkillReconciliationPlan(context.Background(), denyWriter, nil, "user-1", "agent-1", bundle, plan); err == nil {
		t.Fatal("expected nil authorizer to fail closed")
	}
	if writer.creates[0].UserID != "user-1" || writer.creates[0].AgentID != "agent-1" {
		t.Fatalf("wrong create owner: %#v", writer.creates[0])
	}
	if len(writer.patches) != 1 || writer.patches[0].ID != "old-skill" || writer.patches[0].ExpectedVersion != 4 {
		t.Fatalf("unexpected patches: %#v", writer.patches)
	}
	if writer.patches[0].Description == nil || *writer.patches[0].Description != "Patch a reflect-maintained skill." {
		t.Fatalf("patch description not mapped: %#v", writer.patches[0])
	}
	if writer.patches[0].MainFileContent == nil || *writer.patches[0].MainFileContent != "# Patched skill\n" {
		t.Fatalf("patch SKILL.md not mapped: %#v", writer.patches[0])
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

	if _, err := executeSkillReconciliationPlan(context.Background(), writer, &stubSkillAuthorizer{}, "user-1", "agent-1", bundle, plan); err == nil {
		t.Fatal("expected invalid plan error")
	}
	if len(writer.creates) != 0 || len(writer.patches) != 0 {
		t.Fatalf("writer should not be called, got creates=%#v patches=%#v", writer.creates, writer.patches)
	}
}

func TestExecuteSkillPlanCanRetryAfterPartialCommit(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	inner := skills.New(db)
	wantFailure := errors.New("injected second operation failure")
	writer := &failOnceReflectSkillWriter{inner: inner, failCall: 2, err: wantFailure}
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{validSkillCandidate("skill-0001"), validSkillCandidate("skill-0002")},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0001"},
			Name:            "partial-commit-first",
			Description:     "first committed skill",
			MainFileContent: "# First\n",
		},
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0002"},
			Name:            "partial-commit-second",
			Description:     "second committed skill",
			MainFileContent: "# Second\n",
		},
	}}

	if _, err := executeSkillReconciliationPlan(ctx, writer, userID, agentID, bundle, plan); !errors.Is(err, wantFailure) {
		t.Fatalf("first execute error = %v, want injected failure", err)
	}
	written, err := executeSkillReconciliationPlan(ctx, writer, userID, agentID, bundle, plan)
	if err != nil {
		t.Fatalf("retry executeSkillReconciliationPlan: %v", err)
	}
	if len(written) != 2 || written[0].Name != "partial-commit-first" || written[1].Name != "partial-commit-second" {
		t.Fatalf("retry written skills = %#v", written)
	}
	if written[0].Version != 1 || written[1].Version != 1 {
		t.Fatalf("retry versions = %d/%d, want 1/1", written[0].Version, written[1].Version)
	}
}

type fakeReflectSkillWriter struct {
	creates []skills.ReflectSkillCreate
	patches []skills.ReflectSkillPatch
}

type failOnceReflectSkillWriter struct {
	inner    reflectSkillWriter
	calls    int
	failCall int
	err      error
}

func (w *failOnceReflectSkillWriter) CreateReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillCreate) (skills.Skill, error) {
	w.calls++
	if w.calls == w.failCall {
		return skills.Skill{}, w.err
	}
	return w.inner.CreateReflectOwnedUserAgentSkill(ctx, in)
}

func (w *failOnceReflectSkillWriter) PatchReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillPatch) (skills.Skill, error) {
	return w.inner.PatchReflectOwnedUserAgentSkill(ctx, in)
}

func (w *fakeReflectSkillWriter) CreateReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillCreate) (skills.Skill, error) {
	w.creates = append(w.creates, in)
	return skills.Skill{ID: "created-skill", Version: 1}, nil
}

func (w *fakeReflectSkillWriter) PatchReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillPatch) (skills.Skill, error) {
	w.patches = append(w.patches, in)
	return skills.Skill{ID: in.ID, Version: in.ExpectedVersion + 1}, nil
}
