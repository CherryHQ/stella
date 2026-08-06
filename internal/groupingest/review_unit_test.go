package groupingest

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestBuildGroupReviewUnitBoundsFreshAndUsesTypedSubjectCatalog(t *testing.T) {
	prior := []sqlc.CtxGroupMessage{{
		Seq:              1,
		ActorType:        "human",
		ActorID:          "alice",
		ActorDisplayName: pgtype.Text{String: "Old Alice", Valid: true},
		Content:          "prior context",
	}}
	fresh := []sqlc.CtxGroupMessage{
		{
			Seq:              2,
			ActorType:        "human",
			ActorID:          "alice",
			ActorDisplayName: pgtype.Text{String: "Alice", Valid: true},
			Content:          "Alice now owns production releases.",
		},
		{
			Seq:       3,
			ActorType: "agent",
			ActorID:   "support-agent",
			Content:   "Acknowledged.",
		},
		{
			Seq:       4,
			ActorType: "human",
			ActorID:   "bob",
			Content:   strings.Repeat("x", 40),
		},
	}
	unit, err := BuildGroupReviewUnit("group-1", prior, fresh, ReviewUnitOptions{
		FreshTokenBudget: 20,
		PriorTokenBudget: 20,
	})
	if err != nil {
		t.Fatalf("build review unit: %v", err)
	}
	if unit.FreshCount != 2 || unit.ConsumedThroughSeq != 3 {
		t.Fatalf("fresh=%d consumed=%d, want 2 and 3", unit.FreshCount, unit.ConsumedThroughSeq)
	}
	if len(unit.SkippedSeqs) != 0 {
		t.Fatalf("skipped = %v", unit.SkippedSeqs)
	}
	var alice GroupSubjectCatalogEntry
	for _, entry := range unit.Subjects {
		if entry.SubjectID == "alice" {
			alice = entry
		}
	}
	if alice.Subject != memory.GroupFactSubjectHuman || alice.DisplayName != "Alice" {
		t.Fatalf("alice catalog entry = %#v", alice)
	}
	if strings.Contains(unit.Text, `"seq"`) {
		t.Fatalf("review text should not expose event seq as model evidence: %s", unit.Text)
	}
}

func TestBuildGroupReviewUnitSkipsSingleOversizedBoundary(t *testing.T) {
	fresh := []sqlc.CtxGroupMessage{
		{Seq: 1, ActorType: "human", ActorID: "alice", Content: strings.Repeat("x", 100)},
		{Seq: 2, ActorType: "human", ActorID: "alice", Content: "fits"},
	}
	unit, err := BuildGroupReviewUnit("group-1", nil, fresh, ReviewUnitOptions{FreshTokenBudget: 10})
	if err != nil {
		t.Fatalf("build review unit: %v", err)
	}
	if unit.FreshCount != 1 || unit.ConsumedThroughSeq != 2 {
		t.Fatalf("unit = %#v", unit)
	}
	if len(unit.SkippedSeqs) != 1 || unit.SkippedSeqs[0] != 1 {
		t.Fatalf("skipped = %v, want [1]", unit.SkippedSeqs)
	}
}

func TestBuildGroupReviewUnitRedactsSecretsBeforeModelInput(t *testing.T) {
	fresh := []sqlc.CtxGroupMessage{{
		Seq:              1,
		ActorType:        "human",
		ActorID:          "alice",
		ActorDisplayName: pgtype.Text{String: "Alice password=displaysecret", Valid: true},
		Content:          "Use ghp_abcdefghijklmnop1234 and api_key=supersecretvalue",
	}}
	unit, err := BuildGroupReviewUnit("group-1", nil, fresh, ReviewUnitOptions{FreshTokenBudget: 100})
	if err != nil {
		t.Fatalf("build review unit: %v", err)
	}
	for _, secret := range []string{"ghp_", "displaysecret", "supersecretvalue"} {
		if strings.Contains(unit.Text, secret) {
			t.Fatalf("review text leaked %q: %s", secret, unit.Text)
		}
	}
	if !strings.Contains(unit.Text, "[redacted_secret]") {
		t.Fatalf("review text missing redaction marker: %s", unit.Text)
	}
}
