package skill

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

type capturedSkillReader struct {
	*projectionReader
	visible   []ManagedRevision
	masked    []string
	forbidden []string
	captures  int
}

func (r *capturedSkillReader) CaptureVisible(context.Context, ViewContext) (SkillCapture, error) {
	r.captures++
	return SkillCapture{Revisions: r.visible, MaskedNames: append([]string(nil), r.masked...), ForbiddenNames: append([]string(nil), r.forbidden...)}, nil
}

func TestCaptureSkillTurnViewConsumersUseOneCapturedRevision(t *testing.T) {
	digest := strings.Repeat("a", 64)
	identity := Skill{ID: "managed-file", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: "runbook", Description: "old description", Status: SkillStatusActive}
	old := promptRevision(identity, digest, "# old bytes")
	reader := &capturedSkillReader{
		projectionReader: &projectionReader{revisions: map[string]ManagedRevision{identity.ID: old}},
		visible:          []ManagedRevision{old},
	}
	ctx := t.Context()
	view, err := CaptureSkillTurnView(ctx, reader, allowAllSkillReads{}, nil, nil, ViewContext{UserID: identity.UserID, AgentID: identity.AgentID})
	if err != nil {
		t.Fatal(err)
	}
	if reader.captures != 1 {
		t.Fatalf("CaptureVisible calls = %d, want 1", reader.captures)
	}

	reader.revisions[identity.ID] = promptRevision(identity, digest, "# changed source")
	tool := newProjectionTool(t, reader, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	turnCtx := WithSkillTurnView(ctx, view)
	loaded, err := skillAction(tool, "load").Execute(turnCtx, map[string]any{"name": identity.Name})
	if err != nil || !strings.Contains(loaded, "# old bytes") || strings.Contains(loaded, "# changed source") {
		t.Fatalf("captured load = %q, %v", loaded, err)
	}
	if reader.loads != 0 {
		t.Fatalf("captured load reopened mutable revision = %d times", reader.loads)
	}
	search, err := skillAction(tool, "search").Execute(turnCtx, map[string]any{"q": "old description"})
	if err != nil || !strings.Contains(search, identity.Name) {
		t.Fatalf("captured search = %q, %v", search, err)
	}
}

func TestCapturedRevisionCopiesBytesAndModes(t *testing.T) {
	digest := strings.Repeat("b", 64)
	identity := Skill{ID: "managed-file", Scope: "user", UserID: "user-1", Name: "copy", Status: SkillStatusActive}
	source := promptRevision(identity, digest, "bytes")
	reader := &capturedSkillReader{projectionReader: &projectionReader{}, visible: []ManagedRevision{source}}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, nil, nil, ViewContext{UserID: identity.UserID})
	if err != nil {
		t.Fatal(err)
	}
	source.Files[MainFile][0] = 'X'
	source.Modes[MainFile] = fs.FileMode(0o600)
	revision, ok := view.ManagedRevision(identity.ID)
	if !ok || string(revision.Files[MainFile]) != "bytes" || revision.Modes[MainFile] != 0o644 {
		t.Fatalf("captured revision mutated through input: %#v", revision)
	}
}

func TestFilesystemMaskKeepsProjectWinnerButForbiddenHidesIt(t *testing.T) {
	project := &ProjectSnapshot{skills: []Skill{{ID: "project:same", Scope: "project", Name: "same", Description: "project runbook", Status: SkillStatusActive}}}
	reader := &capturedSkillReader{projectionReader: &projectionReader{}, masked: []string{"same"}}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, project, nil, ViewContext{})
	if err != nil {
		t.Fatal(err)
	}
	tool := newProjectionTool(t, reader, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	search, err := skillAction(tool, "search").Execute(WithSkillTurnView(t.Context(), view), map[string]any{"q": "project runbook"})
	if err != nil || !strings.Contains(search, "same") {
		t.Fatalf("ordinary masked project winner = %q, %v", search, err)
	}

	reader.forbidden = []string{"same"}
	forbiddenView, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, project, nil, ViewContext{})
	if err != nil {
		t.Fatal(err)
	}
	search, err = skillAction(tool, "search").Execute(WithSkillTurnView(t.Context(), forbiddenView), map[string]any{"q": "project runbook"})
	if err != nil || search != noInstalledSkills {
		t.Fatalf("forbidden project winner = %q, %v", search, err)
	}
}

func TestFilesystemForbiddenMasksManagedAndPackageWinners(t *testing.T) {
	identity := Skill{ID: "managed-file", Scope: "user", UserID: "user-1", Name: "same", Description: "managed", Status: SkillStatusActive}
	digest := strings.Repeat("f", 64)
	reader := &capturedSkillReader{
		projectionReader: &projectionReader{},
		visible:          []ManagedRevision{promptRevision(identity, digest, "managed")},
		forbidden:        []string{"same"},
	}
	packageRefs := []PackageSkillRef{{PackageID: "pkg-a", PackageDigest: "sha256:" + digest, Name: "same", Masked: true}, {PackageID: "pkg-b", PackageDigest: "sha256:" + digest, Name: "same"}}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, nil, packageRefs, ViewContext{UserID: identity.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ManagedIdentities()) != 0 {
		t.Fatalf("forbidden managed winner survived: %#v", view.ManagedIdentities())
	}
	tool := newProjectionTool(t, reader, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{}).WithPluginVisibility([]string{"pkg"}, []string{"pkg"})
	search, err := skillAction(tool, "search").Execute(WithSkillTurnView(t.Context(), view), map[string]any{"q": "same"})
	if err != nil || search != noInstalledSkills {
		t.Fatalf("forbidden package winner survived: %q, %v", search, err)
	}
}

func TestCaptureSkillTurnViewPinsDigestAndContextCopies(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	identity := Skill{ID: "managed-1", Scope: "user", UserID: "user-1", Name: "runbook"}
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{
			identity.ID: {Skill: Skill{ID: identity.ID, Scope: identity.Scope, UserID: identity.UserID, Name: identity.Name, ContentDigest: digest}},
		},
	}
	packageDigest := "sha256:" + digest
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, nil, []PackageSkillRef{{PackageID: "pkg", PackageDigest: packageDigest, Name: "pkg-skill"}}, ViewContext{UserID: identity.UserID})
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
	if len(copy.PackageSkills()) != 1 || copy.PackageSkills()[0].PackageDigest != packageDigest {
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

func TestCaptureFailedPackageDoesNotMaskHealthyManagedWinner(t *testing.T) {
	digest := strings.Repeat("1", 64)
	managed := Skill{ID: "managed-foo", Scope: "user", UserID: "user-1", Name: "foo", Description: "managed foo"}
	reader := &projectionReader{
		identities: []Skill{managed},
		revisions: map[string]ManagedRevision{
			managed.ID: promptRevision(managed, digest, "managed bytes"),
		},
	}
	view, err := CaptureSkillTurnView(t.Context(), reader, allowAllSkillReads{}, nil, []PackageSkillRef{{
		PackageID: "failed-package", PackageDigest: "sha256:" + digest, Name: "foo", Masked: true,
	}}, ViewContext{UserID: managed.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.ManagedIdentities(); len(got) != 1 || got[0].Name != managed.Name {
		t.Fatalf("managed winner = %#v, want %q", got, managed.Name)
	}
	tool := newProjectionTool(t, reader, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	ctx := WithSkillTurnView(t.Context(), view)
	search, err := skillAction(tool, "search").Execute(ctx, map[string]any{"q": "managed foo"})
	if err != nil || !strings.Contains(search, `"name": "foo"`) {
		t.Fatalf("managed search = %q, %v", search, err)
	}
	loaded, err := skillAction(tool, "load").Execute(ctx, map[string]any{"name": "foo"})
	if err != nil || !strings.Contains(loaded, "managed bytes") {
		t.Fatalf("managed load = %q, %v", loaded, err)
	}
}

func TestCaptureFailedPackageDoesNotMaskProjectWinner(t *testing.T) {
	digest := strings.Repeat("2", 64)
	project := &ProjectSnapshot{skills: []Skill{{ID: "project:foo", Scope: "project", Name: "foo", Description: "project foo", Status: SkillStatusActive}}}
	view, err := CaptureSkillTurnView(t.Context(), &projectionReader{}, allowAllSkillReads{}, project, []PackageSkillRef{{
		PackageID: "failed-package", PackageDigest: "sha256:" + digest, Name: "foo", Masked: true,
	}}, ViewContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.ProjectSnapshot(); got == nil || len(got.list()) != 1 || got.list()[0].ID != "project:foo" {
		t.Fatalf("project winner = %#v, want project foo", got)
	}
	if len(view.ManagedIdentities()) != 0 || len(view.MaskedSkillNames()) != 0 {
		t.Fatalf("failed package altered project precedence: managed=%#v masked=%v", view.ManagedIdentities(), view.MaskedSkillNames())
	}
}

type nilDecisionAuthorizer struct{}

func (nilDecisionAuthorizer) BeginRead(context.Context) (SkillReadDecision, error) { return nil, nil }

func TestValidateSkillTurnSelectionKeepsPrecedenceAndRejectsPackageTie(t *testing.T) {
	digest := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	packageDigest := "sha256:" + digest
	view, err := NewSkillTurnView(nil, []ManagedSkillRef{{Identity: Skill{ID: "managed", Scope: "user", Name: "same", ContentDigest: digest}}}, []PackageSkillRef{{PackageID: "a", PackageDigest: packageDigest, Name: "same"}, {PackageID: "b", PackageDigest: packageDigest, Name: "same"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err != nil {
		t.Fatalf("package shadowed by managed Skill should not conflict: %v", err)
	}
	view, err = NewSkillTurnView(nil, nil, []PackageSkillRef{{PackageID: "a", PackageDigest: packageDigest, Name: "same"}, {PackageID: "b", PackageDigest: packageDigest, Name: "same"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err == nil {
		t.Fatal("same-layer package Skills were silently accepted")
	}
}

func TestFailedPackageStillConflictsButExplicitDisableRemovesCandidate(t *testing.T) {
	digest := "abababababababababababababababababababababababababababababababab"
	packageDigest := "sha256:" + digest
	failed := PackageSkillRef{PackageID: "failed", PackageDigest: packageDigest, Name: "same", Masked: true}
	active := PackageSkillRef{PackageID: "active", PackageDigest: packageDigest, Name: "same"}
	view, err := NewSkillTurnView(nil, nil, []PackageSkillRef{failed, active}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err == nil {
		t.Fatal("failed package silently removed same-layer conflict")
	}
	failed.Disabled = true
	failed.Masked = false
	view, err = NewSkillTurnView(nil, nil, []PackageSkillRef{failed, active}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err != nil {
		t.Fatalf("explicitly disabled package kept conflict: %v", err)
	}
}

func TestExternalPackageConflictsWithBuiltinAtSharedPackageLayer(t *testing.T) {
	digest := "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	view, err := NewSkillTurnView(nil, nil, []PackageSkillRef{{PackageID: "external", PackageDigest: "sha256:" + digest, Name: "email"}, {PackageID: "builtin-email", PackageDigest: "sha256:" + digest, Name: "email", Builtin: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err == nil {
		t.Fatal("external package silently shadowed builtin Skill")
	}
}

func TestFailedExternalPackageMasksBuiltinFallback(t *testing.T) {
	digest := "dededededededededededededededededededededededededededededededede"
	view, err := NewSkillTurnView(nil, nil, []PackageSkillRef{{PackageID: "external", PackageDigest: "sha256:" + digest, Name: "email", Masked: true}, {PackageID: "builtin-email", PackageDigest: "sha256:" + digest, Name: "email", Builtin: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillTurnSelection(view); err == nil {
		t.Fatal("failed external package silently shadowed active builtin Skill")
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
