package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/skill"
)

func TestFilesystemReflectBundleAuthorizesEvidenceBackedPatch(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000323"
	const agentID = "file-reflect-reconcile-agent"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,'file-reflect-reconcile@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent(id,name,workspace) VALUES($1,'File reflect reconcile','')`, agentID); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := skill.NewFileStore(db, manager)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, skill.ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "bundle-backed", Description: "original workflow", MainFileContent: "# Original\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := validSkillCandidate("skill-0001")
	bundle, err := buildSkillRelatedBundle(ctx, store, userID, agentID, []skillCandidate{candidate}, []skillRelatedSelection{{
		CandidateRef: candidate.Ref,
		Related:      []skillRelatedHint{{SkillID: created.ID, Relation: skillRelationPatchableGap}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.RelatedRecords) != 1 {
		t.Fatalf("unexpected evidence-backed bundle: %#v", bundle.RelatedRecords)
	}
	if strings.Contains(string(bundle.RelatedRecords[0].Skill.Metadata), "created_by") {
		t.Fatalf("file Skill bundle unexpectedly carries forgeable created_by metadata: %s", bundle.RelatedRecords[0].Skill.Metadata)
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:           skillOperationPatch,
		CandidateRefs:       []CandidateRef{candidate.Ref},
		TargetSkillID:       created.ID,
		ExpectedSkillDigest: created.ContentDigest,
		Description:         "updated workflow",
		MainFileContent:     "# Updated\n",
	}}}
	if err := validateSkillReconciliationPlan(bundle, plan); err != nil {
		t.Fatalf("evidence-backed file Skill patch rejected: %v", err)
	}
	written, err := executeSkillReconciliationPlan(ctx, store, &stubSkillAuthorizer{}, userID, agentID, bundle, plan, skillProvenanceInput{
		Context:   testReflectProvenanceContext(),
		Decisions: []skillCandidateDecision{testSkillCandidateDecision(candidate, 0.95)},
	})
	if err != nil {
		t.Fatalf("execute evidence-backed file Skill patch: %v", err)
	}
	if len(written) != 1 || written[0].ID != created.ID || written[0].ContentDigest == created.ContentDigest {
		t.Fatalf("written file Skill = %#v, want changed revision for %s", written, created.ID)
	}
}
