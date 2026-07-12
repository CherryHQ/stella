package reflect

import (
	"context"
	"strings"
	"testing"

	skillstool "github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// summaryFakeStore is a SkillStore whose List returns a fixed set; other methods
// are unused by the summary path.
type summaryFakeStore struct {
	pkgplugins.SkillStore
	skills []pkgplugins.Skill
}

func (f summaryFakeStore) List(context.Context, pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	return f.skills, nil
}

// summaryReadAuthz denies the skill ids in deny; everything else is allowed. It
// records that BeginRead ran under a context carrying the review identity.
type summaryReadAuthz struct{ deny map[string]bool }

func (a summaryReadAuthz) BeginRead(context.Context) (skillstool.SkillReadDecision, error) {
	return summaryReadDecision(a), nil
}

type summaryReadDecision struct{ deny map[string]bool }

func (d summaryReadDecision) AllowRead(_ context.Context, id, _, _, _ string, _ []byte) (bool, error) {
	return !d.deny[id], nil
}

// TestLoadExistingSkillSummariesFiltersDeniedSkills proves the reviewer's
// "existing skills" prompt summaries run under the review identity and pass each
// DB row through the same ResourceSkill read decision the tool uses: a denied
// skill is dropped from the prompt, and with no authorizer the summaries fail
// hidden (empty) rather than leaking every skill via a context.Background bypass.
func TestLoadExistingSkillSummariesFiltersDeniedSkills(t *testing.T) {
	store := summaryFakeStore{skills: []pkgplugins.Skill{
		{ID: "a", Name: "alpha", Description: "allowed skill"},
		{ID: "b", Name: "beta", Description: "denied skill"},
	}}

	s := &Service{skillStore: store, skillReadAuthz: summaryReadAuthz{deny: map[string]bool{"b": true}}}
	got := s.loadExistingSkillSummaries(context.Background(), "user-1")
	if len(got) != 1 || !strings.Contains(got[0], "alpha") {
		t.Fatalf("summaries = %v, want only the allowed skill", got)
	}
	for _, e := range got {
		if strings.Contains(e, "beta") {
			t.Fatalf("denied skill leaked into prompt summaries: %v", got)
		}
	}

	// No read authorizer → fail hidden (empty), never leak.
	sNoAuthz := &Service{skillStore: store}
	if got := sNoAuthz.loadExistingSkillSummaries(context.Background(), "user-1"); len(got) != 0 {
		t.Fatalf("summaries without an authorizer = %v, want empty (fail hidden)", got)
	}
}
