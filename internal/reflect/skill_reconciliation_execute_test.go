package reflect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skill"
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
			Skill: skill.Skill{
				ID:            "old-skill",
				Scope:         "user_agent",
				Status:        "active",
				Version:       4,
				ContentDigest: testSkillContentDigest,
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
			Operation:           skillOperationPatch,
			CandidateRefs:       []CandidateRef{"skill-0002"},
			TargetSkillID:       "old-skill",
			ExpectedSkillDigest: testSkillContentDigest,
			Description:         "Patch a reflect-maintained skill.",
			MainFileContent:     "# Patched skill\n",
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
	if len(writer.patches) != 1 || writer.patches[0].ID != "old-skill" || writer.patches[0].ExpectedDigest != testSkillContentDigest {
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
		Operation:       skillOperationPatch,
		CandidateRefs:   []CandidateRef{"skill-0001"},
		TargetSkillID:   "missing-skill",
		MainFileContent: "# Invalid\n",
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

func TestExecuteSkillPlanCanRetryAfterPartialCommit(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	inner := newReflectFileSkillStore(t, db)
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
	provenance := skillProvenanceInput{
		Context: testReflectProvenanceContext(),
		Decisions: []skillCandidateDecision{
			testSkillCandidateDecision(bundle.Candidates[0], 0.91),
			testSkillCandidateDecision(bundle.Candidates[1], 0.92),
		},
	}

	partial, err := executeSkillReconciliationPlan(ctx, writer, &stubSkillAuthorizer{}, userID, agentID, bundle, plan, provenance)
	if !errors.Is(err, wantFailure) {
		t.Fatalf("first execute error = %v, want injected failure", err)
	}
	if len(partial) != 1 || partial[0].Name != "partial-commit-first" {
		t.Fatalf("partial writes = %#v, want the committed first skill", partial)
	}
	var firstMetadata []byte
	if err := db.QueryRow(ctx, `
		SELECT sc.metadata
		FROM skill_changelog sc
		WHERE COALESCE(sc.resource_id, sc.skill_id) = $1
		ORDER BY sc.created_at DESC, sc.id DESC
		LIMIT 1
	`, partial[0].ID).Scan(&firstMetadata); err != nil {
		t.Fatalf("read first partial changelog: %v", err)
	}
	var firstProvenance reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(firstMetadata, &firstProvenance); err != nil {
		t.Fatalf("decode first partial provenance: %v", err)
	}
	if firstProvenance.ReflectProvenance.OperationRef != "skill-0001" {
		t.Fatalf("first partial operation ref = %q", firstProvenance.ReflectProvenance.OperationRef)
	}
	var firstHistory int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM skill_changelog
		WHERE COALESCE(resource_id, skill_id) = $1
	`, partial[0].ID).Scan(&firstHistory); err != nil {
		t.Fatalf("count first partial changelog: %v", err)
	}
	if firstHistory != 1 {
		t.Fatalf("first partial changelog rows = %d, want 1", firstHistory)
	}
	visibleBeforeRetry, err := inner.ListIdentityByScope(ctx, "user_agent", userID, agentID)
	if err != nil {
		t.Fatalf("list filesystem skills before retry: %v", err)
	}
	for _, candidate := range visibleBeforeRetry {
		if candidate.Name == "partial-commit-second" {
			t.Fatalf("failed second operation committed filesystem Skill: %#v", candidate)
		}
	}

	// A later line retry gets a new run ID. The already committed first create is
	// an idempotent no-op, while the second create records the new attempt.
	provenance.Context.RunID = "run-2"
	written, err := executeSkillReconciliationPlan(ctx, writer, &stubSkillAuthorizer{}, userID, agentID, bundle, plan, provenance)
	if err != nil {
		t.Fatalf("retry executeSkillReconciliationPlan: %v", err)
	}
	if len(written) != 2 || written[0].Name != "partial-commit-first" || written[1].Name != "partial-commit-second" {
		t.Fatalf("retry written skills = %#v", written)
	}
	if written[0].Version != 0 || written[1].Version != 0 {
		t.Fatalf("retry filesystem versions = %d/%d, want 0/0", written[0].Version, written[1].Version)
	}
	visibleAfterRetry, err := inner.ListIdentityByScope(ctx, "user_agent", userID, agentID)
	if err != nil || len(visibleAfterRetry) != 2 {
		t.Fatalf("filesystem skills after retry = %#v, err=%v; want exactly two resources", visibleAfterRetry, err)
	}
	var firstRetryMetadata []byte
	if err := db.QueryRow(ctx, `
		SELECT sc.metadata
		FROM skill_changelog sc
		WHERE COALESCE(sc.resource_id, sc.skill_id) = $1
		ORDER BY sc.created_at DESC, sc.id DESC
		LIMIT 1
	`, partial[0].ID).Scan(&firstRetryMetadata); err != nil {
		t.Fatalf("read first retry changelog: %v", err)
	}
	var firstRetryProvenance reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(firstRetryMetadata, &firstRetryProvenance); err != nil {
		t.Fatalf("decode first retry provenance: %v", err)
	}
	if !bytes.Equal(firstRetryMetadata, firstMetadata) {
		t.Fatalf("first retry provenance changed: got %s, want %s", firstRetryMetadata, firstMetadata)
	}
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM skill_changelog
		WHERE COALESCE(resource_id, skill_id) = $1
	`, partial[0].ID).Scan(&firstHistory); err != nil {
		t.Fatalf("count first retry changelog: %v", err)
	}
	if firstHistory != 1 {
		t.Fatalf("first retry changelog rows = %d, want 1", firstHistory)
	}
	var secondHistory int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM skill_changelog
		WHERE COALESCE(resource_id, skill_id) = $1
	`, written[1].ID).Scan(&secondHistory); err != nil {
		t.Fatalf("count second retry changelog: %v", err)
	}
	if secondHistory != 1 {
		t.Fatalf("second retry changelog rows = %d, want 1", secondHistory)
	}
	var secondMetadata []byte
	if err := db.QueryRow(ctx, `
		SELECT sc.metadata
		FROM skill_changelog sc
		WHERE COALESCE(sc.resource_id, sc.skill_id) = $1
		ORDER BY sc.created_at DESC, sc.id DESC
		LIMIT 1
	`, written[1].ID).Scan(&secondMetadata); err != nil {
		t.Fatalf("read second retry changelog: %v", err)
	}
	var secondProvenance reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(secondMetadata, &secondProvenance); err != nil {
		t.Fatalf("decode second retry provenance: %v", err)
	}
	if secondProvenance.ReflectProvenance.OperationRef != "skill-0002" ||
		secondProvenance.ReflectProvenance.RunID != "run-2" {
		t.Fatalf("second retry provenance header = %#v", secondProvenance.ReflectProvenance.reflectProvenanceHeader)
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
	creates []skill.ReflectSkillCreate
	patches []skill.ReflectSkillPatch
}

type failOnceReflectSkillWriter struct {
	inner    reflectSkillWriter
	calls    int
	failCall int
	err      error
}

func (w *failOnceReflectSkillWriter) CreateReflectOwnedUserAgentSkill(ctx context.Context, in skill.ReflectSkillCreate) (skill.Skill, error) {
	w.calls++
	if w.calls == w.failCall {
		return skill.Skill{}, w.err
	}
	return w.inner.CreateReflectOwnedUserAgentSkill(ctx, in)
}

func (w *failOnceReflectSkillWriter) PatchReflectOwnedUserAgentSkill(ctx context.Context, in skill.ReflectSkillPatch) (skill.Skill, error) {
	return w.inner.PatchReflectOwnedUserAgentSkill(ctx, in)
}

func (w *fakeReflectSkillWriter) CreateReflectOwnedUserAgentSkill(_ context.Context, in skill.ReflectSkillCreate) (skill.Skill, error) {
	w.creates = append(w.creates, in)
	return skill.Skill{ID: "created-skill", Version: 1}, nil
}

func (w *fakeReflectSkillWriter) PatchReflectOwnedUserAgentSkill(_ context.Context, in skill.ReflectSkillPatch) (skill.Skill, error) {
	w.patches = append(w.patches, in)
	return skill.Skill{ID: in.ID, Version: 2}, nil
}
