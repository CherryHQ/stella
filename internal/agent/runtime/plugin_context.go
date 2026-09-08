package runtime

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
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
	snapshot      plugin.Snapshot
	view          pkgplugins.SessionPluginView
	mcp           *pkgplugins.MCPToolSnapshot
	authority     authz.Authority
	fileBased     bool
	fileResources []plugin.FileResource
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
	return PluginContext{snapshot: snapshot, view: view, authority: snapshot.Authority()}, nil
}

// NewFilePluginContext builds a runner context directly from the trusted file
// resource capture. File resources are already selected by DiscoverResources;
// this constructor deliberately performs no database lookup or fallback.
func NewFilePluginContext(authority authz.Authority, resources []plugin.FileResource) (PluginContext, error) {
	if !authority.Valid() {
		return PluginContext{}, authz.ErrForbidden
	}
	for _, resource := range resources {
		if resource.Key.ID() == "" || !fileResourceVisibleToAuthority(resource.Key, authority) {
			return PluginContext{}, authz.ErrForbidden
		}
	}
	view, err := projectFileSessionPluginView(resources)
	if err != nil {
		return PluginContext{}, err
	}
	return PluginContext{
		view:          view,
		authority:     authority,
		fileBased:     true,
		fileResources: cloneFileResources(resources),
	}, nil
}

func fileResourceVisibleToAuthority(key plugin.ResourceKey, authority authz.Authority) bool {
	switch key.Scope {
	case plugin.ScopeSystem:
		return true
	case plugin.ScopeSystemAgent:
		// A user request may execute an explicitly authorized agent and receives
		// its system-agent root through the trusted root capability. An agent
		// actor is further pinned to its own agent root.
		if authority.Kind() == authz.ActorAgent || authority.Kind() == authz.ActorGroupAgent {
			return string(authority.AgentID()) == key.AgentID
		}
		return authority.Kind() == authz.ActorUser || authority.Kind() == authz.ActorSystem
	case plugin.ScopeUser:
		if authority.Kind() == authz.ActorUser || authority.Kind() == authz.ActorAgent {
			return string(authority.UserID()) == key.UserID
		}
		return false
	case plugin.ScopeUserAgent:
		if authority.Kind() == authz.ActorAgent {
			return string(authority.UserID()) == key.UserID && string(authority.AgentID()) == key.AgentID
		}
		if authority.Kind() == authz.ActorUser {
			return string(authority.UserID()) == key.UserID
		}
		return false
	default:
		return false
	}
}

// Authority returns the trusted actor that produced this context.
func (c PluginContext) Authority() authz.Authority {
	if c.authority.Valid() {
		return c.authority
	}
	return c.snapshot.Authority()
}

// IsFileBased reports whether this context was projected from filesystem
// resources rather than the legacy database snapshot.
func (c PluginContext) IsFileBased() bool { return c.fileBased }

// InfrastructureContext drops captured file resources before a runner is
// retained. File resources are turn-scoped observations; keeping them on a
// cached runner would pin the first filesystem snapshot and its package
// declarations across later admissions. Authority and the file marker remain
// so the cache can still identify the confined execution shape.
func (c PluginContext) InfrastructureContext() PluginContext {
	if !c.fileBased {
		return c
	}
	return PluginContext{authority: c.authority, fileBased: true}
}

// FileResources returns an independent copy of the captured resources. The
// captured content is immutable; all mutable declarations and diagnostics are
// copied before crossing the context boundary.
func (c PluginContext) FileResources() []plugin.FileResource {
	return cloneFileResources(c.fileResources)
}

// Snapshot returns the authority-bound plugin snapshot captured for this
// runner.
func (c PluginContext) Snapshot() plugin.Snapshot { return c.snapshot }

// SessionPluginView returns a defensive copy of the session setup and plugin
// visibility captured for this runner.
func (c PluginContext) SessionPluginView() pkgplugins.SessionPluginView {
	return applyPreparationResult(cloneSessionPluginView(c.view))
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
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, authority: c.authority, fileBased: c.fileBased, fileResources: c.fileResources, oauthResult: c.oauthResult.Clone()}
}

// OAuthPreparationResult returns the latest admission-time OAuth predicate.
// The result contains readiness only; callers must not treat it as token
// material or use it to bypass the filtered SessionPluginView.
func (c PluginContext) OAuthPreparationResult() pkgplugins.PluginPreparationResult {
	return c.oauthResult.Clone()
}

// WithOAuthPreparationResult records the per-admission OAuth predicate and
// applies it to the public view. Later CLI preparation can merge into the
// public PackageResults while preserving this cache identity input.
func (c PluginContext) WithOAuthPreparationResult(result pkgplugins.PluginPreparationResult) PluginContext {
	view := cloneSessionPluginView(c.view)
	view.PackageResults = c.view.PackageResults.Merge(result)
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, authority: c.authority, fileBased: c.fileBased, fileResources: c.fileResources, oauthResult: result.Clone()}
}

// WithPreparationResult publishes one all-or-nothing result to every runner
// consumer. The authored selection remains intact for identity and retry; the
// public SessionPluginView derives a filtered copy for each consumer.
func (c PluginContext) WithPreparationResult(result pkgplugins.PluginPreparationResult) PluginContext {
	view := cloneSessionPluginView(c.view)
	view.PackageResults = c.view.PackageResults.Merge(result)
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp, authority: c.authority, fileBased: c.fileBased, fileResources: c.fileResources, oauthResult: c.oauthResult.Clone()}
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
	if c.fileBased {
		return c.fileExecutionSummary(view)
	}
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
		} else if status, prepared := packagePreparationStatus(c.oauthResult, definition.ID); prepared {
			item.Authorization = "ready"
			if !status.Ready {
				item.Authorization = "unavailable"
				if status.Reason != "" {
					item.Failures = append(item.Failures, status.Reason)
				}
			}
		}
		if status, prepared := packagePreparationStatus(c.view.PackageResults, definition.ID); prepared {
			switch {
			case !status.Ready:
				item.Readiness = "unavailable"
				if status.Reason != "" && !slices.Contains(item.Failures, status.Reason) {
					item.Failures = append(item.Failures, status.Reason)
				}
			case c.packagePreparationComplete(definition.ID, len(payload.OAuth) > 0):
				item.Readiness = "ready"
			default:
				item.Readiness = "unknown"
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
				if !binaryEvidenceMatchesSpec(evidence, spec) {
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
	appendExecutionSkills(result, view, packageVersions)
	if len(result.Plugins) == 0 && len(result.Skills) == 0 {
		return nil
	}
	return result
}

func (c PluginContext) fileExecutionSummary(view *skill.SkillTurnView) *ai.ExecutionSummary {
	visible := make(map[string]struct{}, len(c.view.ExposedPluginIDs)+len(c.view.PackageResults.Packages))
	for _, id := range c.view.ExposedPluginIDs {
		visible[id] = struct{}{}
	}
	// SessionPluginView is filtered for execution consumers after preparation.
	// Keep package IDs from the raw result as receipt entries even when a failed
	// package was removed from ExposedPluginIDs by that projection.
	for _, status := range c.view.PackageResults.Packages {
		visible[status.PluginID] = struct{}{}
	}
	packageVersions := make(map[string]string)
	for _, resource := range c.fileResources {
		if resource.Key.Kind == plugin.ResourcePlugin && resource.Package != nil {
			packageVersions[resource.Key.ID()] = resource.Package.Manifest.Version
		}
	}
	result := &ai.ExecutionSummary{}
	for _, resource := range c.fileResources {
		id := resource.Key.ID()
		if _, ok := visible[id]; !ok || (resource.Key.Kind != plugin.ResourcePlugin && resource.Key.Kind != plugin.ResourceMCP) {
			continue
		}
		item := ai.ExecutionPlugin{PluginID: id, PackageDigest: resource.Digest, Source: "file", ConfigScope: string(resource.Key.Scope), Authorization: "not_required", Readiness: "unknown"}
		if resource.Package != nil {
			item.PackageVersion = resource.Package.Manifest.Version
			if resource.Package.Extension != nil && len(resource.Package.Extension.OAuth) > 0 {
				item.Authorization = "unknown"
				if status, prepared := packagePreparationStatus(c.oauthResult, id); prepared {
					item.Authorization = "ready"
					if !status.Ready {
						item.Authorization = "unavailable"
						if status.Reason != "" {
							item.Failures = append(item.Failures, status.Reason)
						}
					}
				}
			}
		} else if resource.Key.Kind == plugin.ResourcePlugin {
			item.Authorization = "unknown"
		}
		if status, prepared := packagePreparationStatus(c.view.PackageResults, id); prepared {
			switch {
			case !status.Ready:
				item.Readiness = "unavailable"
				if status.Reason != "" {
					item.Failures = append(item.Failures, status.Reason)
				}
			case c.packagePreparationComplete(id, resourcePackageRequiresOAuth(resource)):
				item.Readiness = "ready"
			default:
				item.Readiness = "unknown"
			}
		}
		for _, spec := range c.view.BinarySpecs {
			if spec.PluginID != id {
				continue
			}
			binary := ai.ExecutionBinary{Name: spec.Name, Tool: spec.Tool, RequestedVersion: spec.Version, Source: "package"}
			for _, evidence := range c.view.PackageResults.Binaries {
				if !binaryEvidenceMatchesSpec(evidence, spec) {
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
	appendExecutionSkills(result, view, packageVersions)
	if len(result.Plugins) == 0 && len(result.Skills) == 0 {
		return nil
	}
	return result
}

func packagePreparationStatus(result pkgplugins.PluginPreparationResult, pluginID string) (pkgplugins.PluginPackageStatus, bool) {
	for _, status := range result.Packages {
		if status.PluginID == pluginID {
			return status, true
		}
	}
	return pkgplugins.PluginPackageStatus{}, false
}

func binaryEvidenceMatchesSpec(evidence pkgplugins.PluginBinaryPreparation, spec pkgplugins.PluginBinarySpec) bool {
	return evidence.PluginID == spec.PluginID && evidence.ConfigID == spec.ConfigID &&
		evidence.Scope == spec.Scope && evidence.Revision == spec.Revision &&
		evidence.PackageDigest == spec.PackageDigest && evidence.Name == spec.Name && evidence.Tool == spec.Tool
}

func (c PluginContext) packagePreparationComplete(pluginID string, oauthRequired bool) bool {
	if oauthRequired {
		if _, prepared := packagePreparationStatus(c.oauthResult, pluginID); !prepared {
			return false
		}
	}
	for _, spec := range c.view.BinarySpecs {
		if spec.PluginID != pluginID {
			continue
		}
		matched := false
		for _, evidence := range c.view.PackageResults.Binaries {
			if binaryEvidenceMatchesSpec(evidence, spec) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func resourcePackageRequiresOAuth(resource plugin.FileResource) bool {
	return resource.Package != nil && resource.Package.Extension != nil && len(resource.Package.Extension.OAuth) > 0
}

func appendExecutionSkills(result *ai.ExecutionSummary, view *skill.SkillTurnView, packageVersions map[string]string) {
	if view == nil {
		return
	}
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
		source := "managed"
		pluginID := ""
		if strings.HasPrefix(identity.ID, "file:") {
			source = "file"
			pluginID = identity.ID
		}
		result.Skills = append(result.Skills, ai.ExecutionSkill{
			PluginID: pluginID, Name: identity.Name, Source: source, Scope: identity.Scope, Digest: identity.ContentDigest, State: "selected",
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
		source := "package"
		if candidate.Builtin {
			scope = "system"
		}
		if key, err := plugin.ParseResourceID(candidate.PackageID); err == nil {
			source = "file"
			scope = string(key.Scope)
		}
		result.Skills = append(result.Skills, ai.ExecutionSkill{
			PluginID: candidate.PackageID, Name: candidate.Name, Source: source, Scope: scope, Version: packageVersions[candidate.PackageID],
			Digest: candidate.PackageDigest, State: state,
		})
	}
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
	return (PluginContext{snapshot: c.snapshot, view: c.view, mcp: &copy, authority: c.authority, fileBased: c.fileBased, fileResources: c.fileResources, oauthResult: c.oauthResult.Clone()}).WithMCPResources(snapshot.Directory, snapshot.SuccessfulPluginIDs)
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

func cloneFileResources(resources []plugin.FileResource) []plugin.FileResource {
	if resources == nil {
		return nil
	}
	cloned := make([]plugin.FileResource, len(resources))
	for i, resource := range resources {
		cloned[i] = resource
		if resource.Content != nil {
			content := *resource.Content
			cloned[i].Content = &content
		}
		cloned[i].DisabledTools = slices.Clone(resource.DisabledTools)
		cloned[i].Diagnostics = slices.Clone(resource.Diagnostics)
		if resource.MCP != nil {
			cloned[i].MCP = make(map[string]mcpconfig.Declaration, len(resource.MCP))
			for name, declaration := range resource.MCP {
				declaration.Headers = maps.Clone(declaration.Headers)
				declaration.Scopes = slices.Clone(declaration.Scopes)
				cloned[i].MCP[name] = declaration
			}
		}
		if resource.Package != nil {
			pkg := *resource.Package
			pkg.Manifest.Keywords = slices.Clone(resource.Package.Manifest.Keywords)
			if resource.Package.Manifest.Author != nil {
				author := *resource.Package.Manifest.Author
				pkg.Manifest.Author = &author
			}
			pkg.Skills = slices.Clone(resource.Package.Skills)
			for j := range pkg.Skills {
				pkg.Skills[j].Content = slices.Clone(pkg.Skills[j].Content)
			}
			pkg.MCPServers = slices.Clone(resource.Package.MCPServers)
			for j := range pkg.MCPServers {
				pkg.MCPServers[j].Headers = maps.Clone(resource.Package.MCPServers[j].Headers)
			}
			if resource.Package.Extension != nil {
				extension := *resource.Package.Extension
				extension.Binaries = slices.Clone(resource.Package.Extension.Binaries)
				for j := range extension.Binaries {
					extension.Binaries[j].Options = maps.Clone(resource.Package.Extension.Binaries[j].Options)
					for name, raw := range extension.Binaries[j].Options {
						extension.Binaries[j].Options[name] = slices.Clone(raw)
					}
				}
				extension.SessionEnv = slices.Clone(resource.Package.Extension.SessionEnv)
				extension.OAuth = slices.Clone(resource.Package.Extension.OAuth)
				for j := range extension.OAuth {
					extension.OAuth[j].Scopes = slices.Clone(resource.Package.Extension.OAuth[j].Scopes)
					extension.OAuth[j].Bindings = slices.Clone(resource.Package.Extension.OAuth[j].Bindings)
				}
				extension.MCPAuth = maps.Clone(resource.Package.Extension.MCPAuth)
				for name, auth := range extension.MCPAuth {
					auth.Scopes = slices.Clone(auth.Scopes)
					extension.MCPAuth[name] = auth
				}
				pkg.Extension = &extension
			}
			cloned[i].Package = &pkg
		}
		cloned[i].Skills = slices.Clone(resource.Skills)
		for j := range cloned[i].Skills {
			cloned[i].Skills[j].Content = slices.Clone(cloned[i].Skills[j].Content)
		}
	}
	return cloned
}

// PluginContextBuilder captures all plugin state used to construct one new
// runner. The authority is supplied by trusted runtime identity, never
// reconstructed from a user-controlled model or prompt field.
type PluginContextBuilder func(context.Context, authz.Authority, string) (PluginContext, error)
