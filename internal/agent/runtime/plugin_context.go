package runtime

import (
	"context"
	"maps"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// PluginContext is the immutable plugin state captured while admitting a
// runner. The snapshot and session view are built from the same authority and
// must travel together for the lifetime of the runner and every turn using it.
type PluginContext struct {
	snapshot plugin.Snapshot
	view     pkgplugins.SessionPluginView
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
