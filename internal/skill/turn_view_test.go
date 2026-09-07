package skill

import (
	"context"
	"errors"
	"testing"
)

func TestCaptureSkillTurnViewPinsDigestAndContextCopies(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	identity := Skill{ID: "managed-1", Scope: "user", UserID: "user-1", Name: "runbook"}
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{
			identity.ID: {Skill: Skill{ID: identity.ID, Scope: identity.Scope, UserID: identity.UserID, Name: identity.Name, ContentDigest: digest}},
		},
	}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, nil, []PackageSkillRef{{PackageID: "pkg", PackageDigest: digest, Name: "pkg-skill"}}, ViewContext{UserID: identity.UserID})
	if err != nil {
		t.Fatal(err)
	}
	refs := view.ManagedSkills()
	if len(refs) != 1 || refs[0].Identity.ContentDigest != digest {
		t.Fatalf("managed refs = %#v", refs)
	}
	ctx := WithSkillTurnView(t.Context(), view)
	copy, ok := SkillTurnViewFromContext(ctx)
	if !ok {
		t.Fatal("turn view missing from context")
	}
	copy.ManagedSkills()[0].Identity.Name = "mutated"
	if got := view.ManagedSkills()[0].Identity.Name; got != identity.Name {
		t.Fatalf("view was mutable through a returned copy: %q", got)
	}
	if len(copy.PackageSkills()) != 1 || copy.PackageSkills()[0].PackageDigest != digest {
		t.Fatalf("package refs = %#v", copy.PackageSkills())
	}
}

func TestNewSkillTurnViewRejectsUnpinnedManagedRevision(t *testing.T) {
	_, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: Skill{ID: "id", Name: "name"}}}, nil, nil)
	if !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("error = %v, want ErrInvalidSkillRevision", err)
	}
}

func TestSkillTurnViewOwnsManagedMetadata(t *testing.T) {
	identity := Skill{ID: "managed", Name: "runbook", ContentDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Metadata: []byte(`{"source":"original"}`)}
	view, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: identity}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity.Metadata[0] = '['
	if got := string(view.ManagedSkills()[0].Identity.Metadata); got != `{"source":"original"}` {
		t.Fatalf("input mutation changed captured metadata: %s", got)
	}
}

func TestCaptureSkillTurnViewAuthorizesBeforeReadingRevision(t *testing.T) {
	digest := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	identity := Skill{ID: "managed-2", Scope: "user", UserID: "user-2", Name: "private"}
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions:  map[string]ManagedRevision{identity.ID: {Skill: Skill{ID: identity.ID, Scope: identity.Scope, UserID: identity.UserID, Name: identity.Name, ContentDigest: digest}}},
	}
	view, err := CaptureSkillTurnView(t.Context(), reader, selectedSkillReads{denied: map[string]bool{identity.ID: true}}, nil, nil, ViewContext{UserID: identity.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ManagedSkills()) != 0 || reader.loads != 0 {
		t.Fatalf("view=%#v loads=%d, want denied before Home read", view, reader.loads)
	}
	if _, err := CaptureSkillTurnView(t.Context(), reader, nil, nil, nil, ViewContext{UserID: identity.UserID}); !errors.Is(err, ErrManagedSkillsUnavailable) {
		t.Fatalf("nil authorizer error=%v, want ErrManagedSkillsUnavailable", err)
	}
	if _, err := CaptureSkillTurnView(t.Context(), reader, nilDecisionAuthorizer{}, nil, nil, ViewContext{UserID: identity.UserID}); !errors.Is(err, ErrSkillReadUnavailable) {
		t.Fatalf("nil decision error=%v, want ErrSkillReadUnavailable", err)
	}
}

func TestCaptureSkillTurnViewDoesNotReviveDeniedHigherPrecedenceSkill(t *testing.T) {
	digest := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	high := Skill{ID: "managed-high", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: "same"}
	low := Skill{ID: "managed-low", Scope: "system", Name: "same"}
	reader := &projectionReader{
		identities: []Skill{high, low},
		revisions: map[string]ManagedRevision{
			high.ID: {Skill: Skill{ID: high.ID, Scope: high.Scope, UserID: high.UserID, AgentID: high.AgentID, Name: high.Name, ContentDigest: digest}},
			low.ID:  {Skill: Skill{ID: low.ID, Scope: low.Scope, Name: low.Name, ContentDigest: digest}},
		},
	}
	view, err := CaptureSkillTurnView(t.Context(), reader, selectedSkillReads{denied: map[string]bool{high.ID: true}}, nil, nil, ViewContext{UserID: high.UserID, AgentID: high.AgentID})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(view.ManagedSkills()); got != 0 {
		t.Fatalf("managed refs=%d, want denied winner to mask lower scope", got)
	}
	if reader.loads != 0 {
		t.Fatalf("revision loads=%d, want no lower-scope revival", reader.loads)
	}
	if got := view.MaskedSkillNames(); len(got) != 1 || got[0] != high.Name {
		t.Fatalf("masked names=%v, want [%q]", got, high.Name)
	}
}

func TestCaptureSkillTurnViewDoesNotPrepareShadowedManagedWinner(t *testing.T) {
	identity := Skill{ID: "managed-shadowed", Scope: "user", UserID: "user-1", Name: "same"}
	reader := &projectionReader{identities: []Skill{identity}, revisions: map[string]ManagedRevision{}}
	project := &ProjectSnapshot{skills: []Skill{{ID: "project:same", Scope: "project", Name: "same", Status: SkillStatusActive}}}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, project, nil, ViewContext{UserID: identity.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ManagedSkills()) != 0 || reader.loads != 0 {
		t.Fatalf("view=%#v loads=%d, want project winner without managed Home read", view, reader.loads)
	}
}

type nilDecisionAuthorizer struct{}

func (nilDecisionAuthorizer) BeginRead(context.Context) (SkillReadDecision, error) { return nil, nil }

func TestValidateSkillTurnSelectionKeepsPrecedenceAndRejectsPackageTie(t *testing.T) {
	digest := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	view, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: Skill{ID: "managed", Scope: "user", Name: "same", ContentDigest: digest}}}, []PackageSkillRef{{PackageID: "a", PackageDigest: digest, Name: "same"}, {PackageID: "b", PackageDigest: digest, Name: "same"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err != nil {
		t.Fatalf("package shadowed by managed Skill should not conflict: %v", err)
	}
	view, err = NewSkillTurnView(nil, nil, []PackageSkillRef{{PackageID: "a", PackageDigest: digest, Name: "same"}, {PackageID: "b", PackageDigest: digest, Name: "same"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err == nil {
		t.Fatal("same-layer package Skills were silently accepted")
	}
}

func TestActiveTurnOwnerReleasesWholeView(t *testing.T) {
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	view, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: Skill{ID: "managed", Scope: "user", Name: "same", ContentDigest: digest}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var owner ActiveTurnOwner
	if err := owner.Register("turn-1", view); err != nil {
		t.Fatal(err)
	}
	if got := len(owner.Snapshot()); got != 1 {
		t.Fatalf("active turns=%d, want 1", got)
	}
	if err := owner.Register("turn-1", view); err == nil {
		t.Fatal("duplicate turn registration succeeded")
	}
	owner.Release("turn-1")
	if got := len(owner.Snapshot()); got != 0 {
		t.Fatalf("active turns after release=%d, want 0", got)
	}
}

func TestDisabledManagedWinnerMasksBuiltinWithSameName(t *testing.T) {
	digest := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	view, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: Skill{ID: "system-agent-lark", Scope: "system_agent", AgentID: "agent-1", Name: "lark-cli", ContentDigest: digest}}}, nil, []string{"system_agent:lark-cli"})
	if err != nil {
		t.Fatal(err)
	}
	tool := newProjectionTool(t, &projectionReader{}, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	toolCtx := WithSkillTurnView(t.Context(), view)
	out, err := tool.Search(toolCtx, SkillSearchInput{Q: "lark cli"})
	if err != nil {
		t.Fatal(err)
	}
	if out != noInstalledSkills {
		t.Fatalf("disabled managed winner revived builtin: %v", out)
	}
}
