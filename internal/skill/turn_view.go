package skill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
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

	// captured is the immutable package Skill tree selected during PluginContext
	// admission. A turn must use these bytes directly; nil is retained only for
	// legacy non-turn callers that still use PackageSkillReader.
	captured *PackageSkillRevision
}

// CapturePackageSkillRef projects one package Skill from a FileResource that
// was already captured for the admitting PluginContext. It never opens a
// mutable root or consults the package store.
func CapturePackageSkillRef(resource plugin.FileResource, ref PackageSkillRef) (PackageSkillRef, error) {
	if err := validatePackageSkillRef(ref); err != nil {
		return PackageSkillRef{}, err
	}
	// A masked or disabled declaration is admission evidence only. It has no
	// bytes to capture and must remain visible to precedence/masking logic
	// without turning a failed package into an admission error.
	if ref.Masked || ref.Disabled {
		ref.captured = nil
		return ref, nil
	}
	key, err := plugin.ParseResourceID(ref.PackageID)
	if err != nil || key != resource.Key || resource.Key.Kind != plugin.ResourcePlugin || resource.Content == nil || resource.Package == nil || resource.Disabled || resource.Forbidden {
		return PackageSkillRef{}, ErrInvalidSkillRevision
	}
	if resource.Digest == "" || resource.Content.Digest == "" || resource.Digest != resource.Content.Digest || resource.Digest != ref.PackageDigest {
		return PackageSkillRef{}, ErrInvalidSkillRevision
	}
	declared := false
	for _, skill := range resource.Skills {
		if skill.Name == ref.Name {
			declared = true
			break
		}
	}
	if !declared {
		return PackageSkillRef{}, ErrInvalidSkillRevision
	}
	root, err := fs.Sub(resource.Content.FS(), path.Join("skills", ref.Name))
	if err != nil {
		return PackageSkillRef{}, ErrInvalidSkillRevision
	}
	files := make(map[string][]byte)
	modes := make(map[string]fs.FileMode)
	err = fs.WalkDir(root, ".", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !fs.ValidPath(filename) || filename == "." || entry.Type()&fs.ModeType != 0 {
			return ErrInvalidSkillRevision
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o444 == 0 {
			return ErrInvalidSkillRevision
		}
		content, err := fs.ReadFile(root, filename)
		if err != nil {
			return err
		}
		files[filename] = bytes.Clone(content)
		modes[filename] = info.Mode().Perm()
		return nil
	})
	if err != nil {
		return PackageSkillRef{}, err
	}
	if _, ok := files[MainFile]; !ok {
		return PackageSkillRef{}, ErrInvalidSkillRevision
	}
	capturedRef := ref
	capturedRef.captured = nil
	revision := PackageSkillRevision{Ref: capturedRef, Files: files, Modes: modes}
	ref.captured = &revision
	return ref, nil
}

// ManagedSkillRef pins one managed Skill identity to the exact revision chosen
// during turn admission. The identity remains subject to a fresh read PEP at
// every search/load; this snapshot never extends authorization after revocation.
type ManagedSkillRef struct {
	Identity Skill

	// captured is non-nil for filesystem-backed Skills. The bytes and modes are
	// the only content source for this turn; consumers must not reopen a
	// mutable path or call LoadExactRevision for these refs. Nil means legacy
	// POSIX identity-only behavior.
	captured *ManagedRevision
}

// visibleSkillCapture is intentionally local to the turn admission path. It
// lets the new filesystem store provide one bounded capture without changing
// the temporary IdentityReader contract used by legacy POSIX callers.
type visibleSkillCapture interface {
	CaptureVisible(context.Context, ViewContext) (SkillCapture, error)
}

// CaptureSkillTurnView snapshots the visible managed identities and their
// current revisions at admission. Authorization is deliberately not cached;
// consumers still run the read PEP immediately before each operation.
func CaptureSkillTurnView(ctx context.Context, reader IdentityReader, authorizer SkillReadAuthorizer, project *ProjectSnapshot, packages []PackageSkillRef, vc ViewContext) (SkillTurnView, error) {
	if reader == nil || authorizer == nil {
		return SkillTurnView{}, ErrManagedSkillsUnavailable
	}
	if capture, ok := reader.(visibleSkillCapture); ok {
		return captureFilesystemSkillTurnView(ctx, capture, authorizer, project, packages, vc)
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

func captureFilesystemSkillTurnView(ctx context.Context, capture visibleSkillCapture, authorizer SkillReadAuthorizer, project *ProjectSnapshot, packages []PackageSkillRef, vc ViewContext) (SkillTurnView, error) {
	// BeginRead validates the trusted actor before CaptureVisible opens any
	// user-owned root. An unauthenticated caller can still see immutable
	// project/package/release skills, but receives no filesystem diagnostics.
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

	captured, err := capture.CaptureVisible(ctx, vc)
	if err != nil {
		return SkillTurnView{}, err
	}
	return buildFilesystemSkillTurnView(ctx, decision, project, packages, vc, captured)
}

// CaptureSkillTurnViewFromResources builds a turn view from the exact file
// resources already captured for PluginContext admission. It never opens a
// Home root or falls back to a mutable Skill reader.
func CaptureSkillTurnViewFromResources(ctx context.Context, resources []plugin.FileResource, authorizer SkillReadAuthorizer, project *ProjectSnapshot, packages []PackageSkillRef, vc ViewContext) (SkillTurnView, error) {
	if authorizer == nil {
		return SkillTurnView{}, ErrManagedSkillsUnavailable
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
	captured, err := captureResources(ctx, resources, vc)
	if err != nil {
		return SkillTurnView{}, err
	}
	return buildFilesystemSkillTurnView(ctx, decision, project, packages, vc, captured)
}

func buildFilesystemSkillTurnView(ctx context.Context, decision SkillReadDecision, project *ProjectSnapshot, packages []PackageSkillRef, vc ViewContext, captured SkillCapture) (SkillTurnView, error) {
	project = projectWithoutForbiddenNames(project, captured.ForbiddenNames)
	masked := append(slices.Clone(captured.MaskedNames), captured.ForbiddenNames...)
	revisions := captured.Revisions
	identities := make([]Skill, 0, len(revisions))
	byID := make(map[string]ManagedRevision, len(revisions))
	forbiddenSet := make(map[string]struct{}, len(captured.ForbiddenNames))
	for _, name := range captured.ForbiddenNames {
		forbiddenSet[name] = struct{}{}
	}
	for _, revision := range revisions {
		if !validCapturedManagedRevision(revision) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		if _, forbidden := forbiddenSet[revision.Skill.Name]; forbidden {
			continue
		}
		identities = append(identities, revision.Skill)
		byID[revision.Skill.ID] = revision
	}
	selectionPackages := slices.Clone(packages)
	for i := range selectionPackages {
		if _, forbidden := forbiddenSet[selectionPackages[i].Name]; forbidden {
			selectionPackages[i].Disabled = true
			selectionPackages[i].Masked = false
		}
	}
	selected, err := selectManagedSkillIdentities(project, identities, selectionPackages, masked)
	if err != nil {
		return SkillTurnView{}, err
	}
	managed := make([]ManagedSkillRef, 0, len(selected))
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
		revision, ok := byID[identity.ID]
		if !ok || !sameSkillIdentity(identity, revision.Skill) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		capturedRevision := cloneManagedRevision(revision)
		managed = append(managed, ManagedSkillRef{Identity: capturedRevision.Skill, captured: &capturedRevision})
	}
	masked = append(masked, packageMaskedNamesForSelection(project, selected, selectionPackages, masked)...)
	view, err := newSkillTurnView(project, managed, selectionPackages, vc.DisabledSkillRefs, masked)
	if err != nil {
		return SkillTurnView{}, err
	}
	return view, ValidateSkillTurnSelection(view)
}

// projectWithoutForbiddenNames removes administrator-forbidden project Skills
// before the ordinary mask is attached to the view. Ordinary malformed or
// disabled managed Skills still leave a same-name project winner intact.
func projectWithoutForbiddenNames(project *ProjectSnapshot, names []string) *ProjectSnapshot {
	if project == nil || len(names) == 0 {
		return project
	}
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	clone := &ProjectSnapshot{
		dirs:   maps.Clone(project.dirs),
		files:  maps.Clone(project.files),
		modes:  maps.Clone(project.modes),
		skills: make([]Skill, 0, len(project.skills)),
	}
	for _, skill := range project.skills {
		if _, forbidden := blocked[skill.Name]; forbidden {
			continue
		}
		skill.Metadata = bytes.Clone(skill.Metadata)
		clone.skills = append(clone.skills, skill)
	}
	for name, dir := range clone.dirs {
		if _, forbidden := blocked[name]; !forbidden {
			continue
		}
		delete(clone.dirs, name)
		prefix := dir + "/"
		maps.DeleteFunc(clone.files, func(filename string, _ string) bool { return strings.HasPrefix(filename, prefix) })
		maps.DeleteFunc(clone.modes, func(filename string, _ fs.FileMode) bool { return strings.HasPrefix(filename, prefix) })
	}
	return clone
}

func validCapturedManagedRevision(revision ManagedRevision) bool {
	if revision.Skill.ID == "" || revision.Skill.Name == "" || !validSkillDigest(revision.Skill.ContentDigest) {
		return false
	}
	if len(revision.Files) == 0 || len(revision.Files) != len(revision.Modes) {
		return false
	}
	for filename := range revision.Files {
		mode, ok := revision.Modes[filename]
		if !ok || mode&fs.ModeType != 0 || mode.Perm()&0o444 == 0 || !fs.ValidPath(filename) || filename == "." {
			return false
		}
	}
	_, hasMain := revision.Files[MainFile]
	return hasMain
}

func cloneManagedRevision(revision ManagedRevision) ManagedRevision {
	clone := revision
	clone.Skill.Metadata = bytes.Clone(revision.Skill.Metadata)
	clone.Files = make(map[string][]byte, len(revision.Files))
	for filename, content := range revision.Files {
		clone.Files[filename] = bytes.Clone(content)
	}
	clone.Modes = maps.Clone(revision.Modes)
	return clone
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
		packages: clonePackageSkillRefs(packages),
		disabled: slices.Clone(disabled),
		masked:   slices.Clone(masked),
	}
	for i := range view.managed {
		view.managed[i].Identity.Metadata = slices.Clone(view.managed[i].Identity.Metadata)
		if !validSkillDigest(view.managed[i].Identity.ContentDigest) || view.managed[i].Identity.ID == "" || view.managed[i].Identity.Name == "" {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		if view.managed[i].captured != nil {
			captured := cloneManagedRevision(*view.managed[i].captured)
			view.managed[i].captured = &captured
			if !sameSkillIdentity(view.managed[i].Identity, captured.Skill) || !validCapturedManagedRevision(captured) {
				return SkillTurnView{}, ErrInvalidSkillRevision
			}
		}
	}
	for i := range view.packages {
		ref := &view.packages[i]
		if err := validatePackageSkillRef(*ref); err != nil {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
		if ref.captured == nil {
			continue
		}
		if !samePackageSkillRef(*ref, ref.captured.Ref) || !validCapturedPackageSkillRevision(*ref.captured) {
			return SkillTurnView{}, ErrInvalidSkillRevision
		}
	}
	return view, nil
}

func validPackageSkillPath(value, name string) bool {
	return !path.IsAbs(value) && !strings.Contains(value, `\`) && path.Clean(value) == value && strings.HasPrefix(value, "skills/"+name+"/")
}

func validatePackageSkillRef(ref PackageSkillRef) error {
	if !validPackageID(ref.PackageID) || !validInventoryComponent(ref.Name) || !validPackageDigest(ref.PackageDigest) {
		return ErrInvalidSkillRevision
	}
	if ref.Path != "" && !validPackageSkillPath(ref.Path, ref.Name) {
		return ErrInvalidSkillRevision
	}
	return nil
}

func validPackageID(id string) bool {
	if key, err := plugin.ParseResourceID(id); err == nil {
		return key.Kind == plugin.ResourcePlugin
	}
	return validInventoryComponent(id)
}

func isFilePackageSkillRef(ref PackageSkillRef) bool {
	key, err := plugin.ParseResourceID(ref.PackageID)
	return err == nil && key.Kind == plugin.ResourcePlugin
}

func validCapturedPackageSkillRevision(revision PackageSkillRevision) bool {
	if revision.Ref.captured != nil || validatePackageSkillRef(revision.Ref) != nil {
		return false
	}
	if len(revision.Files) == 0 || len(revision.Files) != len(revision.Modes) {
		return false
	}
	for filename, content := range revision.Files {
		mode, ok := revision.Modes[filename]
		if !ok || !fs.ValidPath(filename) || filename == "." || mode&fs.ModeType != 0 || mode.Perm()&0o444 == 0 || content == nil {
			return false
		}
	}
	_, hasMain := revision.Files[MainFile]
	return hasMain
}

func clonePackageSkillRevision(revision PackageSkillRevision) PackageSkillRevision {
	clone := revision
	clone.Ref.captured = nil
	clone.Files = make(map[string][]byte, len(revision.Files))
	for filename, content := range revision.Files {
		clone.Files[filename] = bytes.Clone(content)
	}
	clone.Modes = maps.Clone(revision.Modes)
	return clone
}

func clonePackageSkillRefs(refs []PackageSkillRef) []PackageSkillRef {
	clone := slices.Clone(refs)
	for i := range clone {
		if clone[i].captured == nil {
			continue
		}
		captured := clonePackageSkillRevision(*clone[i].captured)
		clone[i].captured = &captured
	}
	return clone
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
		if out[i].captured != nil {
			captured := cloneManagedRevision(*out[i].captured)
			out[i].captured = &captured
		}
	}
	return out
}

// ManagedRevision returns a detached copy of the exact bytes captured for a
// filesystem-backed ref. Legacy POSIX refs return false and are loaded through
// the existing revision reader.
func (v SkillTurnView) ManagedRevision(id string) (ManagedRevision, bool) {
	for _, ref := range v.managed {
		if ref.captured != nil && ref.Identity.ID == id {
			return cloneManagedRevision(*ref.captured), true
		}
	}
	return ManagedRevision{}, false
}

// ManagedIdentities returns the selected managed rows with their exact digest.
func (v SkillTurnView) ManagedIdentities() []Skill {
	out := make([]Skill, len(v.managed))
	for i, ref := range v.managed {
		out[i] = ref.Identity
		out[i].Metadata = slices.Clone(ref.Identity.Metadata)
	}
	return out
}

// PackageSkills returns defensive copies of package-owned Skill references.
func (v SkillTurnView) PackageSkills() []PackageSkillRef { return clonePackageSkillRefs(v.packages) }

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
		packages: clonePackageSkillRefs(v.packages),
		disabled: slices.Clone(v.disabled),
		masked:   slices.Clone(v.masked),
	}
	for i := range clone.managed {
		clone.managed[i].Identity.Metadata = slices.Clone(clone.managed[i].Identity.Metadata)
		if clone.managed[i].captured != nil {
			captured := cloneManagedRevision(*clone.managed[i].captured)
			clone.managed[i].captured = &captured
		}
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
