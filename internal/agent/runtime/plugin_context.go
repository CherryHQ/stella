package runtime

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type preparedPluginContextKey struct{}

func withPreparedPluginContext(ctx context.Context, pluginContext PluginContext) context.Context {
	return context.WithValue(ctx, preparedPluginContextKey{}, pluginContext)
}

func preparedPluginContext(ctx context.Context) (PluginContext, bool) {
	pluginContext, ok := ctx.Value(preparedPluginContextKey{}).(PluginContext)
	return pluginContext, ok
}

// PluginContext is the immutable plugin state captured while admitting a
// runner. The snapshot and session view are built from the same authority and
// must travel together for the lifetime of the runner and every turn using it.
type PluginContext struct {
	snapshot plugin.Snapshot
	view     pkgplugins.SessionPluginView
	mcp      *pkgplugins.MCPToolSnapshot
	// oauthResult is the admission-time OAuth predicate. It is separate from
	// view.PackageResults because the latter also contains CLI preparation;
	// cache identity must notice an OAuth permission change without making a
	// successful CLI install part of every fresh-context comparison.
	oauthResult pkgplugins.PluginPreparationResult
}

// NewPluginContext derives every Agent resource from the same frozen snapshot.
// Callers cannot supply a view from another authority or configuration revision.
func NewPluginContext(snapshot plugin.Snapshot) (PluginContext, error) {
	view, err := projectSessionPluginView(snapshot)
	if err != nil {
		return PluginContext{}, err
	}
	return PluginContext{snapshot: snapshot, view: view}, nil
}

// Snapshot returns the authority-bound plugin snapshot captured for this
// runner.
func (c PluginContext) Snapshot() plugin.Snapshot { return c.snapshot }

// SessionPluginView returns a defensive copy of the session setup and plugin
// visibility captured for this runner.
func (c PluginContext) SessionPluginView() pkgplugins.SessionPluginView {
	return applyPreparationResult(cloneSessionPluginView(c.view))
}

// SameIdentity compares the complete immutable plugin boundary. The snapshot
// carries definition content and resolved configuration; the session view
// carries the selected MCP directory and the successful tool/package set.
// Comparing both prevents a shallow cache marker from accepting a runner whose
// visible capability graph changed underneath admission.
func (c PluginContext) SameIdentity(other PluginContext) bool {
	if !sameOAuthReadiness(c.oauthResult, other.oauthResult) {
		return false
	}
	left, right := c.view, other.view
	// Preparation is an admission outcome, not authored selection identity. A
	// fresh context is built before the sandbox can report CLI installation;
	// comparing this field would make every successful build look stale. The
	// immutable snapshot and MCP observation remain part of the cache identity.
	left.PackageResults = pkgplugins.PluginPreparationResult{}
	right.PackageResults = pkgplugins.PluginPreparationResult{}
	return reflect.DeepEqual(c.snapshot, other.snapshot) && reflect.DeepEqual(left, right)
}

// WithMCPResources returns a copy whose view includes the one-shot
// observation-backed MCP projection used to build the runner. Keeping this
// projection beside the authored snapshot makes cache identity cover both
// durable configuration and the successful remote capability set.
func (c PluginContext) WithMCPResources(directory []pkgplugins.MCPDirectoryEntry, successful []string) PluginContext {
	view := cloneSessionPluginView(c.view)
	view.MCPDirectory = slices.Clone(directory)
	for i := range view.MCPDirectory {
		view.MCPDirectory[i].Tools = slices.Clone(view.MCPDirectory[i].Tools)
		for j := range view.MCPDirectory[i].Tools {
			view.MCPDirectory[i].Tools[j].InputSchema = clonePluginOptions(view.MCPDirectory[i].Tools[j].InputSchema)
			view.MCPDirectory[i].Tools[j].Annotations = clonePluginOptions(view.MCPDirectory[i].Tools[j].Annotations)
		}
	}
	view.SuccessfulPluginIDs = slices.Clone(successful)
	slices.Sort(view.SuccessfulPluginIDs)
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, oauthResult: c.oauthResult.Clone()}
}

// OAuthPreparationResult returns the latest admission-time OAuth predicate.
// The result contains readiness only; callers must not treat it as token
// material or use it to bypass the filtered SessionPluginView.
func (c PluginContext) OAuthPreparationResult() pkgplugins.PluginPreparationResult {
	return c.oauthResult.Clone()
}

// HasFailedPackagePreparation reports whether the published runner was built
// with any OAuth-ready package omitted by later CLI preparation. The cache
// uses it to retry optional CLI failures on the next admission while leaving
// runners with stable OAuth failures reusable.
func (c PluginContext) HasFailedPackagePreparation() bool {
	for _, status := range c.view.PackageResults.Packages {
		if status.Ready {
			continue
		}
		// OAuth failures are already represented by oauthResult and are retried
		// by the next admission check. Only a package that was OAuth-ready but
		// failed later CLI preparation should retire an otherwise usable runner.
		if !c.oauthResult.Status(status.PluginID).Ready {
			continue
		}
		return true
	}
	return false
}

// WithOAuthPreparationResult records the per-admission OAuth predicate and
// applies it to the public view. Later CLI preparation can merge into the
// public PackageResults while preserving this cache identity input.
func (c PluginContext) WithOAuthPreparationResult(result pkgplugins.PluginPreparationResult) PluginContext {
	view := cloneSessionPluginView(c.view)
	view.PackageResults = c.view.PackageResults.Merge(result)
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, oauthResult: result.Clone()}
}

// WithPreparationResult publishes one all-or-nothing result to every runner
// consumer. The authored selection remains intact for identity and retry; the
// public SessionPluginView derives a filtered copy for each consumer.
func (c PluginContext) WithPreparationResult(result pkgplugins.PluginPreparationResult) PluginContext {
	view := cloneSessionPluginView(c.view)
	view.PackageResults = c.view.PackageResults.Merge(result)
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, oauthResult: c.oauthResult.Clone()}
}

// SelectedPluginSkillSpecs returns the immutable selected Skill declarations,
// including failed packages, for the Skill resolver to apply its own narrow
// failure masking without losing same-layer identity information.
func (c PluginContext) SelectedPluginSkillSpecs() []pkgplugins.PluginSkillSpec {
	return slices.Clone(c.view.SkillSpecs)
}

// SelectedPluginBinarySpecs returns the authored binary candidates before any
// package readiness projection. Admission uses it to validate cross-package
// alias conflicts before a failed package can disappear from the set.
func (c PluginContext) SelectedPluginBinarySpecs() []pkgplugins.PluginBinarySpec {
	return slices.Clone(c.view.BinarySpecs)
}

// ExecutionSummaryForTurn projects one immutable admission snapshot into the
// small, secret-free record persisted on the user anchor. The optional Skill
// view is the exact winner/mask decision captured for this turn.
func (c PluginContext) ExecutionSummaryForTurn(view *skill.SkillTurnView) *ai.ExecutionSummary {
	byID := make(map[string]struct{}, len(c.view.ExposedPluginIDs))
	for _, id := range c.view.ExposedPluginIDs {
		byID[id] = struct{}{}
	}
	packageVersions := make(map[string]string, len(c.snapshot.Definitions()))
	result := &ai.ExecutionSummary{Plugins: make([]ai.ExecutionPlugin, 0, len(byID))}
	for _, definition := range c.snapshot.Definitions() {
		payload, err := plugin.DecodeResourcePayload(definition.Spec, "execution summary")
		if err != nil {
			continue
		}
		packageVersions[definition.ID] = payload.Version
		if _, selected := byID[definition.ID]; !selected {
			continue
		}
		resolved, ok := c.snapshot.Get(definition.ID)
		if !ok {
			continue
		}
		item := ai.ExecutionPlugin{
			PluginID:       definition.ID,
			PackageVersion: payload.Version,
			PackageDigest:  payload.ContentDigest,
			Source:         string(definition.Source),
			Authorization:  "unknown",
			Readiness:      "unknown",
		}
		if payload.Content != nil {
			item.PackageDigest = payload.Content.Digest
		}
		if resolved.Config != nil {
			item.ConfigID = resolved.Config.ID
			item.ConfigScope = string(resolved.Config.Scope)
			item.ConfigRevision = resolved.Config.Revision
		}
		if len(payload.OAuth) == 0 {
			item.Authorization = "not_required"
		} else if status := c.oauthResult.Status(definition.ID); len(c.oauthResult.Packages) > 0 {
			item.Authorization = "ready"
			if !status.Ready {
				item.Authorization = "unavailable"
				if status.Reason != "" {
					item.Failures = append(item.Failures, status.Reason)
				}
			}
		}
		status := c.view.PackageResults.Status(definition.ID)
		if len(c.view.PackageResults.Packages) > 0 {
			if status.Ready {
				item.Readiness = "ready"
			} else {
				item.Readiness = "unavailable"
				if status.Reason != "" && !slices.Contains(item.Failures, status.Reason) {
					item.Failures = append(item.Failures, status.Reason)
				}
			}
		}
		for _, spec := range c.view.BinarySpecs {
			if spec.PluginID != definition.ID {
				continue
			}
			binary := ai.ExecutionBinary{Name: spec.Name, Tool: spec.Tool, RequestedVersion: spec.Version, Source: "package"}
			// PackageResults carries immutable installer evidence when the CLI
			// preparation step has completed. An authored range remains a range;
			// keep it in RequestedVersion and leave Version empty if no resolved
			// artifact version was observed.
			for _, evidence := range c.view.PackageResults.Binaries {
				if evidence.PluginID != spec.PluginID || evidence.ConfigID != spec.ConfigID ||
					evidence.Scope != spec.Scope || evidence.Revision != spec.Revision ||
					evidence.Name != spec.Name || evidence.Tool != spec.Tool {
					continue
				}
				binary.RequestedVersion = evidence.RequestedVersion
				binary.ResolvedVersion = evidence.ResolvedVersion
				binary.Backend = evidence.Backend
				binary.SelectionIdentity = evidence.SelectionIdentity
				break
			}
			item.Binaries = append(item.Binaries, binary)
		}
		result.Plugins = append(result.Plugins, item)
	}
	if view != nil {
		selectedProject := make(map[string]struct{})
		for _, candidate := range view.ProjectSkills() {
			selectedProject[candidate.Name] = struct{}{}
			result.Skills = append(result.Skills, ai.ExecutionSkill{
				Name: candidate.Name, Source: "project", Scope: "project", Digest: candidate.ContentDigest, State: "selected",
			})
		}
		selectedManaged := make(map[string]struct{})
		for _, candidate := range view.ManagedSkills() {
			identity := candidate.Identity
			result.Skills = append(result.Skills, ai.ExecutionSkill{
				Name: identity.Name, Source: "managed", Scope: identity.Scope, Digest: identity.ContentDigest, State: "selected",
			})
			selectedManaged[identity.Name] = struct{}{}
		}
		maskedNames := make(map[string]struct{}, len(view.MaskedSkillNames()))
		for _, name := range view.MaskedSkillNames() {
			maskedNames[name] = struct{}{}
		}
		disabledNames := make(map[string]struct{})
		for _, ref := range view.DisabledSkillRefs() {
			if name, ok := strings.CutPrefix(ref, "system:"); ok {
				disabledNames[name] = struct{}{}
			}
		}
		for _, candidate := range view.PackageSkills() {
			state := "selected"
			if _, shadowed := selectedProject[candidate.Name]; shadowed {
				state = "overridden"
			} else if _, shadowed := selectedManaged[candidate.Name]; shadowed {
				state = "overridden"
			} else if candidate.Masked || candidate.Disabled {
				state = "masked"
			} else if _, masked := maskedNames[candidate.Name]; masked {
				state = "masked"
			} else if candidate.Builtin {
				if _, disabled := disabledNames[candidate.Name]; disabled {
					state = "masked"
				}
			}
			scope := "package"
			if candidate.Builtin {
				scope = "system"
			}
			result.Skills = append(result.Skills, ai.ExecutionSkill{
				PluginID: candidate.PackageID, Name: candidate.Name, Source: "package", Scope: scope, Version: packageVersions[candidate.PackageID],
				Digest: candidate.PackageDigest, State: state,
			})
		}
	}
	if len(result.Plugins) == 0 && len(result.Skills) == 0 {
		return nil
	}
	return result
}

func applyPreparationResult(view pkgplugins.SessionPluginView) pkgplugins.SessionPluginView {
	if len(view.PackageResults.Packages) == 0 {
		return view
	}
	failed := make(map[string]struct{}, len(view.PackageResults.Packages))
	for _, status := range view.PackageResults.Packages {
		if !status.Ready {
			failed[status.PluginID] = struct{}{}
		}
	}
	keep := func(pluginID string) bool {
		if pluginID == "" {
			return true
		}
		_, unavailable := failed[pluginID]
		return !unavailable
	}
	view.ExposedPluginIDs = slices.DeleteFunc(view.ExposedPluginIDs, func(id string) bool { return !keep(id) })
	view.SessionEnvSpecs = slices.DeleteFunc(view.SessionEnvSpecs, func(spec pkgplugins.SessionEnvSpec) bool { return !keep(spec.PluginID) })
	view.BinarySpecs = slices.DeleteFunc(view.BinarySpecs, func(spec pkgplugins.PluginBinarySpec) bool { return !keep(spec.PluginID) })
	// Keep failed package Skill declarations in the turn projection. Skill
	// selection uses them as masked same-layer candidates, so a failed package
	// cannot silently uncover a builtin Skill with the same name. The Skill
	// capture path consumes this raw declaration list; other resource surfaces
	// below continue to receive only ready package resources.
	view.PromptSections = slices.DeleteFunc(view.PromptSections, func(section pkgplugins.SystemPromptSection) bool { return !keep(section.PluginID) })
	view.MCPDirectory = slices.DeleteFunc(view.MCPDirectory, func(entry pkgplugins.MCPDirectoryEntry) bool { return !keep(entry.PluginID) })
	view.SuccessfulPluginIDs = slices.DeleteFunc(view.SuccessfulPluginIDs, func(id string) bool { return !keep(id) })
	return view
}

// WithMCPToolSnapshot attaches the exact provider result that produced the
// directory identity. Runner construction can reuse these tools instead of
// querying observations a second time.
func (c PluginContext) WithMCPToolSnapshot(snapshot pkgplugins.MCPToolSnapshot) PluginContext {
	copy := pkgplugins.MCPToolSnapshot{Tools: slices.Clone(snapshot.Tools)}
	return (PluginContext{snapshot: c.snapshot, view: c.view, mcp: &copy, oauthResult: c.oauthResult.Clone()}).WithMCPResources(snapshot.Directory, snapshot.SuccessfulPluginIDs)
}

func (c PluginContext) MCPToolSnapshot() (pkgplugins.MCPToolSnapshot, bool) {
	if c.mcp == nil {
		return pkgplugins.MCPToolSnapshot{}, false
	}
	copy := *c.mcp
	copy.Tools = slices.Clone(copy.Tools)
	view := cloneSessionPluginView(c.view)
	copy.Directory = view.MCPDirectory
	copy.SuccessfulPluginIDs = view.SuccessfulPluginIDs
	return copy, true
}

func cloneSessionPluginView(view pkgplugins.SessionPluginView) pkgplugins.SessionPluginView {
	view.RegisteredPluginIDs = slices.Clone(view.RegisteredPluginIDs)
	view.ExposedPluginIDs = slices.Clone(view.ExposedPluginIDs)
	view.SessionEnvSpecs = slices.Clone(view.SessionEnvSpecs)
	for i := range view.SessionEnvSpecs {
		view.SessionEnvSpecs[i].OAuthScopes = slices.Clone(view.SessionEnvSpecs[i].OAuthScopes)
	}
	view.PromptSections = slices.Clone(view.PromptSections)
	view.BinarySpecs = slices.Clone(view.BinarySpecs)
	for i := range view.BinarySpecs {
		view.BinarySpecs[i].Options = clonePluginOptions(view.BinarySpecs[i].Options)
	}
	view.SkillSpecs = slices.Clone(view.SkillSpecs)
	view.PackageRequirements = slices.Clone(view.PackageRequirements)
	for i := range view.PackageRequirements {
		view.PackageRequirements[i].OAuth = slices.Clone(view.PackageRequirements[i].OAuth)
		for j := range view.PackageRequirements[i].OAuth {
			view.PackageRequirements[i].OAuth[j].Scopes = slices.Clone(view.PackageRequirements[i].OAuth[j].Scopes)
			view.PackageRequirements[i].OAuth[j].Bindings = slices.Clone(view.PackageRequirements[i].OAuth[j].Bindings)
		}
	}
	view.PackageResults = view.PackageResults.Clone()
	view.MCPDirectory = slices.Clone(view.MCPDirectory)
	for i := range view.MCPDirectory {
		view.MCPDirectory[i].Tools = slices.Clone(view.MCPDirectory[i].Tools)
		for j := range view.MCPDirectory[i].Tools {
			view.MCPDirectory[i].Tools[j].InputSchema = clonePluginOptions(view.MCPDirectory[i].Tools[j].InputSchema)
			view.MCPDirectory[i].Tools[j].Annotations = clonePluginOptions(view.MCPDirectory[i].Tools[j].Annotations)
		}
	}
	view.SuccessfulPluginIDs = slices.Clone(view.SuccessfulPluginIDs)
	return view
}

func sameOAuthReadiness(left, right pkgplugins.PluginPreparationResult) bool {
	leftByID := make(map[string]bool, len(left.Packages))
	for _, status := range left.Packages {
		leftByID[status.PluginID] = status.Ready
	}
	rightByID := make(map[string]bool, len(right.Packages))
	for _, status := range right.Packages {
		rightByID[status.PluginID] = status.Ready
	}
	return maps.Equal(leftByID, rightByID)
}

func clonePluginOptions(options map[string]any) map[string]any {
	if options == nil {
		return nil
	}
	cloned := maps.Clone(options)
	for key, value := range cloned {
		cloned[key] = clonePluginOption(value)
	}
	return cloned
}

func clonePluginOption(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return clonePluginOptions(value)
	case []any:
		cloned := slices.Clone(value)
		for i, item := range cloned {
			cloned[i] = clonePluginOption(item)
		}
		return cloned
	case map[string]string:
		return maps.Clone(value)
	case []string:
		return slices.Clone(value)
	default:
		return value
	}
}

// PluginContextBuilder captures all plugin state used to construct one new
// runner. The authority is supplied by trusted runtime identity, never
// reconstructed from a user-controlled model or prompt field.
type PluginContextBuilder func(context.Context, authz.Authority, string) (PluginContext, error)
