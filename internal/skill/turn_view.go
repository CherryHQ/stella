package skill

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/authz"
)

var (
	ErrSkillSelectionConflict = errors.New("skills: same-layer Skill name conflict")
	ErrSkillTurnIDRequired    = errors.New("skills: turn owner requires a turn ID")
)

// PackageSkillRef identifies a Skill declared by the immutable PluginContext
// captured for a runner. The package owns the bytes; a turn only carries this
// reference so consumers cannot re-resolve a mutable package catalog.
type PackageSkillRef struct {
	PackageID string
	// PackageDigest is the published asset-tree digest used to resolve the
	// immutable package directory, not the mutable definition revision.
	PackageDigest string
	Name          string
	Path          string
	Description   string
	Disabled      bool
	Builtin       bool
	// Masked records a package preparation failure. It is deliberately smaller
	// than a per-Skill status machine: the admission layer marks every Skill in
	// a failed package, preserving same-name masking for this turn.
	Masked bool
}

// ManagedSkillRef pins one managed Skill identity to the exact revision chosen
// during turn admission. The identity remains subject to a fresh read PEP at
// every search/load; this snapshot never extends authorization after revocation.
type ManagedSkillRef struct {
	Identity Skill
}

// CaptureSkillTurnView snapshots the visible managed identities and their
// current revisions at admission. Authorization is deliberately not cached;
// consumers still run the read PEP immediately before each operation.
func CaptureSkillTurnView(ctx context.Context, reader IdentityReader, authorizer SkillReadAuthorizer, project *ProjectSnapshot, packages []PackageSkillRef, vc ViewContext) (SkillTurnView, error) {
	if reader == nil || authorizer == nil {
		return SkillTurnView{}, ErrManagedSkillsUnavailable
	}
	identities, err := listManagedIdentitiesWhenAvailable(ctx, reader, vc)
	if err != nil {
		return SkillTurnView{}, err
	}
	// Resolve precedence from the complete identity catalog before applying
	// the read PEP. If a higher-precedence winner is denied, consumers must not
	// revive a lower-precedence implementation with the same name.
	selected, err := selectManagedSkillIdentities(project, identities, packages, nil)
	if err != nil {
		return SkillTurnView{}, err
	}
	decision, err := authorizer.BeginRead(ctx)
	if errors.Is(err, authz.ErrUnauthenticated) {
		view, viewErr := newSkillTurnView(project, nil, packages, vc.DisabledSkillRefs, packageMaskedNamesForSelection(project, nil, packages, nil))
		if viewErr != nil {
			return SkillTurnView{}, viewErr
		}
		return view, ValidateSkillTurnSelection(view)
	}
	if err != nil {
		return SkillTurnView{}, err
	}
	if decision == nil {
		return SkillTurnView{}, ErrSkillReadUnavailable
	}
	managed := make([]ManagedSkillRef, 0, len(selected))
	masked := make([]string, 0)
	for _, identity := range selected {
		if isDisabledIdentity(identity, vc.DisabledSkillRefs) {
			masked = append(masked, identity.Name)
			continue
		}
		canRead, err := decision.AllowRead(ctx, identity.ID, identity.Scope, identity.UserID, identity.AgentID)
		if err != nil {
			return SkillTurnView{}, err
		}
		if !canRead {
			masked = append(masked, identity.Name)
			continue
		}
		revision, err := reader.LoadCurrentRevision(ctx, identity)
		if err != nil {
			// A selected winner failing preparation is terminal for this turn;
			// do not silently revive a lower-precedence package or Skill.
			return SkillTurnView{}, err
		}
		if !sameSkillIdentity(identity, revision.Skill) || !validSkillDigest(revision.Skill.ContentDigest) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		managed = append(managed, ManagedSkillRef{Identity: revision.Skill})
	}
	masked = append(masked, packageMaskedNamesForSelection(project, selected, packages, masked)...)
	return newSkillTurnView(project, managed, packages, vc.DisabledSkillRefs, masked)
}

func selectManagedSkillIdentities(project *ProjectSnapshot, managed []Skill, packages []PackageSkillRef, masked []string) ([]Skill, error) {
	shadowed := make(map[string]struct{})
	packageMasked := make(map[string]struct{})
	for _, candidate := range packages {
		if candidate.Masked && candidate.Name != "" {
			packageMasked[candidate.Name] = struct{}{}
		}
	}
	if project != nil {
		for _, candidate := range project.list() {
			shadowed[candidate.Name] = struct{}{}
		}
	}
	for _, name := range masked {
		if _, packageFailure := packageMasked[name]; !packageFailure {
			shadowed[name] = struct{}{}
		}
	}
	byName := make(map[string]Skill)
	for _, candidate := range managed {
		if _, ok := shadowed[candidate.Name]; ok {
			continue
		}
		previous, ok := byName[candidate.Name]
		if !ok || managedScopePriority(candidate.Scope) < managedScopePriority(previous.Scope) {
			byName[candidate.Name] = candidate
			continue
		}
		if managedScopePriority(candidate.Scope) == managedScopePriority(previous.Scope) {
			return nil, errors.Join(ErrSkillSelectionConflict, fmt.Errorf("managed:%s and managed:%s", previous.ID, candidate.ID))
		}
	}
	selected := make([]Skill, 0, len(byName))
	for _, candidate := range byName {
		selected = append(selected, candidate)
	}
	packageNames := make(map[string]struct{})
	builtinNames := make(map[string]struct{})
	for _, candidate := range packages {
		if candidate.Disabled || candidate.Name == "" {
			continue
		}
		if candidate.Builtin {
			builtinNames[candidate.Name] = struct{}{}
			continue
		}
		if _, shadowed := shadowed[candidate.Name]; shadowed {
			continue
		}
		if _, shadowed := byName[candidate.Name]; shadowed {
			continue
		}
		if _, exists := packageNames[candidate.Name]; exists {
			return nil, errors.Join(ErrSkillSelectionConflict, fmt.Errorf("package Skill %q", candidate.Name))
		}
		packageNames[candidate.Name] = struct{}{}
	}
	for name := range packageNames {
		if _, conflict := builtinNames[name]; conflict {
			return nil, errors.Join(ErrSkillSelectionConflict, fmt.Errorf("package Skill %q conflicts with builtin Skill", name))
		}
	}
	return selected, nil
}

func packageMaskedNamesForSelection(project *ProjectSnapshot, managed []Skill, packages []PackageSkillRef, masked []string) []string {
	shadowed := make(map[string]struct{})
	if project != nil {
		for _, candidate := range project.list() {
			shadowed[candidate.Name] = struct{}{}
		}
	}
	for _, candidate := range managed {
		shadowed[candidate.Name] = struct{}{}
	}
	for _, name := range masked {
		delete(shadowed, name)
	}
	seen := make(map[string]struct{})
	for _, ref := range packages {
		if !ref.Masked || ref.Name == "" {
			continue
		}
		if _, ok := shadowed[ref.Name]; ok {
			continue
		}
		seen[ref.Name] = struct{}{}
	}
	return slices.Sorted(maps.Keys(seen))
}

func isDisabledIdentity(identity Skill, disabled []string) bool {
	ref, ok := PolicyRef(ResolvedSkill{Skill: identity})
	return ok && slices.Contains(disabled, ref)
}

// SkillTurnView is the immutable Skill selection for one admitted turn. Project
// is an already captured bounded snapshot; managed and package entries are
// defensive copies and never contain a filesystem capability.
type SkillTurnView struct {
	project  *ProjectSnapshot
	managed  []ManagedSkillRef
	packages []PackageSkillRef
	disabled []string
	masked   []string
}

// ActiveTurnOwner records whole admitted turn views, rather than maintaining a
// second per-resource lease/refcount model. Cleanup consults this owner while a
// turn is active; releasing the turn removes every managed digest atomically.
type ActiveTurnOwner struct {
	mu    sync.Mutex
	turns map[string]SkillTurnView
}

// Register records one admitted turn before it can open managed bytes.
func (o *ActiveTurnOwner) Register(turnID string, view SkillTurnView) error {
	if o == nil || turnID == "" {
		return ErrSkillTurnIDRequired
	}
	if err := ValidateSkillTurnSelection(view); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.turns == nil {
		o.turns = make(map[string]SkillTurnView)
	}
	if _, exists := o.turns[turnID]; exists {
		return fmt.Errorf("skills: turn %q is already registered", turnID)
	}
	o.turns[turnID] = view.Clone()
	return nil
}

// Release removes one completed or canceled turn. Unknown IDs are idempotent.
func (o *ActiveTurnOwner) Release(turnID string) {
	if o == nil || turnID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.turns, turnID)
}

// Snapshot returns the active turn views for cleanup reachability checks.
func (o *ActiveTurnOwner) Snapshot() []SkillTurnView {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]SkillTurnView, 0, len(o.turns))
	for _, view := range o.turns {
		out = append(out, view.Clone())
	}
	return out
}

// NewSkillTurnView builds a defensive turn view from trusted admission data.
// Managed references must carry a valid digest; callers should capture the
// current revision before constructing the view.
func NewSkillTurnView(project *ProjectSnapshot, managed []ManagedSkillRef, packages []PackageSkillRef, disabled []string) (SkillTurnView, error) {
	return newSkillTurnView(project, managed, packages, disabled, nil)
}

func newSkillTurnView(project *ProjectSnapshot, managed []ManagedSkillRef, packages []PackageSkillRef, disabled, masked []string) (SkillTurnView, error) {
	view := SkillTurnView{
		project:  project,
		managed:  slices.Clone(managed),
		packages: slices.Clone(packages),
		disabled: slices.Clone(disabled),
		masked:   slices.Clone(masked),
	}
	for i := range view.managed {
		view.managed[i].Identity.Metadata = slices.Clone(view.managed[i].Identity.Metadata)
		if !validSkillDigest(view.managed[i].Identity.ContentDigest) || view.managed[i].Identity.ID == "" || view.managed[i].Identity.Name == "" {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
	}
	for _, ref := range view.packages {
		if !validInventoryComponent(ref.PackageID) || !validInventoryComponent(ref.Name) || !validPackageDigest(ref.PackageDigest) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		if ref.Path != "" && !validPackageSkillPath(ref.Path, ref.Name) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
	}
	return view, nil
}

func validPackageSkillPath(value, name string) bool {
	return !path.IsAbs(value) && !strings.Contains(value, `\`) && path.Clean(value) == value && strings.HasPrefix(value, "skills/"+name+"/")
}

func validPackageDigest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:") && validSkillDigest(strings.TrimPrefix(digest, "sha256:"))
}

func packageDigestHex(digest string) string {
	return strings.TrimPrefix(digest, "sha256:")
}

// ProjectSnapshot returns the bounded project snapshot selected for this turn.
func (v SkillTurnView) ProjectSnapshot() *ProjectSnapshot { return v.project }

// ProjectSkills returns the exact project Skill identities captured for this
// turn. The metadata is immutable and contains no filesystem capability.
func (v SkillTurnView) ProjectSkills() []Skill {
	if v.project == nil {
		return nil
	}
	return v.project.list()
}

// ManagedSkills returns defensive copies of the exact managed selections.
func (v SkillTurnView) ManagedSkills() []ManagedSkillRef {
	out := slices.Clone(v.managed)
	for i := range out {
		out[i].Identity.Metadata = slices.Clone(out[i].Identity.Metadata)
	}
	return out
}

// ManagedIdentities returns the selected managed rows with their exact digest.
func (v SkillTurnView) ManagedIdentities() []Skill {
	refs := v.ManagedSkills()
	out := make([]Skill, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Identity)
	}
	return out
}

// PackageSkills returns defensive copies of package-owned Skill references.
func (v SkillTurnView) PackageSkills() []PackageSkillRef { return slices.Clone(v.packages) }

// DisabledSkillRefs returns the policy snapshot used during selection.
func (v SkillTurnView) DisabledSkillRefs() []string { return slices.Clone(v.disabled) }

// MaskedSkillNames are selected names whose winner is unavailable by policy;
// lower-precedence candidates must not be revived for this turn.
func (v SkillTurnView) MaskedSkillNames() []string { return slices.Clone(v.masked) }

// ValidateSkillTurnSelection enforces the fixed precedence boundary before
// prompt/tool consumers run: project > managed > package. A disabled package
// is removed before package conflict checking. A masked package remains in
// same-layer conflict checks, then blocks lower builtin fallback.
func ValidateSkillTurnSelection(view SkillTurnView) error {
	_, err := selectManagedSkillIdentities(view.ProjectSnapshot(), view.ManagedIdentities(), view.PackageSkills(), view.MaskedSkillNames())
	return err
}

func managedScopePriority(scope string) int {
	switch scope {
	case "user_agent":
		return 1
	case "user":
		return 2
	case "system_agent":
		return 3
	case "system":
		return 4
	default:
		return 5
	}
}

// Clone returns a detached view suitable for passing to another consumer.
func (v SkillTurnView) Clone() SkillTurnView {
	clone := SkillTurnView{
		project:  v.project,
		managed:  slices.Clone(v.managed),
		packages: slices.Clone(v.packages),
		disabled: slices.Clone(v.disabled),
		masked:   slices.Clone(v.masked),
	}
	for i := range clone.managed {
		clone.managed[i].Identity.Metadata = slices.Clone(clone.managed[i].Identity.Metadata)
	}
	return clone
}

type skillTurnViewContextKey struct{}

// WithSkillTurnView binds one immutable selection to a turn context.
func WithSkillTurnView(ctx context.Context, view SkillTurnView) context.Context {
	return context.WithValue(ctx, skillTurnViewContextKey{}, view.Clone())
}

// SkillTurnViewFromContext returns a defensive copy of the turn selection.
func SkillTurnViewFromContext(ctx context.Context) (SkillTurnView, bool) {
	if ctx == nil {
		return SkillTurnView{}, false
	}
	view, ok := ctx.Value(skillTurnViewContextKey{}).(SkillTurnView)
	if !ok {
		return SkillTurnView{}, false
	}
	return view.Clone(), true
}
