package runtime

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
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
	return cloneSessionPluginView(c.view)
}

// SameIdentity compares the complete immutable plugin boundary. The snapshot
// carries definition content and resolved configuration; the session view
// carries the selected MCP directory and the successful tool/package set.
// Comparing both prevents a shallow cache marker from accepting a runner whose
// visible capability graph changed underneath admission.
func (c PluginContext) SameIdentity(other PluginContext) bool {
	return reflect.DeepEqual(c.snapshot, other.snapshot) && reflect.DeepEqual(c.view, other.view)
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
	return PluginContext{snapshot: c.snapshot, view: view, mcp: c.mcp}
}

// WithMCPToolSnapshot attaches the exact provider result that produced the
// directory identity. Runner construction can reuse these tools instead of
// querying observations a second time.
func (c PluginContext) WithMCPToolSnapshot(snapshot pkgplugins.MCPToolSnapshot) PluginContext {
	copy := pkgplugins.MCPToolSnapshot{Tools: slices.Clone(snapshot.Tools)}
	return (PluginContext{snapshot: c.snapshot, view: c.view, mcp: &copy}).WithMCPResources(snapshot.Directory, snapshot.SuccessfulPluginIDs)
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

// PluginContextBuilder captures all plugin state used to construct one new
// runner. The authority is supplied by trusted runtime identity, never
// reconstructed from a user-controlled model or prompt field.
type PluginContextBuilder func(context.Context, authz.Authority, string) (PluginContext, error)
