package manifestplugins

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
)

// safeOrgToolBackends is the allowlist of mise backends an org may use for cli
// plugins. Every entry installs a precompiled binary by download only. Backends
// that build or run arbitrary code at install time (asdf, vfox, cargo, go, npm,
// pipx, gem, ...) are rejected: org cli tools are installed on the host, outside
// any sandbox, so a code-executing backend would be host RCE driven by org input.
var safeOrgToolBackends = map[string]bool{
	"github": true,
	"gitlab": true,
	"ubi":    true,
	"aqua":   true,
	"http":   true,
}

// orgToolKeyAllowed reports whether an org-supplied mise tool key uses an
// allowlisted precompiled-binary backend. Keys without an explicit safe backend
// prefix — including bare registry names, which may resolve to a code-executing
// backend — are rejected.
func orgToolKeyAllowed(key string) bool {
	backend, _, ok := strings.Cut(key, ":")
	if !ok {
		return false
	}
	return safeOrgToolBackends[backend]
}

// PluginLister lists CLI plugin rows for the org carried in ctx.
type PluginLister interface {
	ListPluginsByKind(ctx context.Context, kind string) ([]config.Plugin, error)
}

// OrgCLISyncer installs an org's self-contained mise config: the builtin base
// tools plus the org's own kind=cli plugins. Installs land in the shared
// MISE_DATA_DIR; the per-org scope config lets runtime shims resolve them.
type OrgCLISyncer struct {
	store      PluginLister
	stellaHome string
}

// NewOrgCLISyncer wires a syncer over the plugin store and stella home.
func NewOrgCLISyncer(store PluginLister, stellaHome string) *OrgCLISyncer {
	return &OrgCLISyncer{store: store, stellaHome: stellaHome}
}

// SyncOrgCLITools writes and installs the org's scope config. ctx must carry the
// org ID so ListPluginsByKind returns that org's rows. The builtin base is
// always included so the org's shims resolve builtin tools, not just org extras.
func (s *OrgCLISyncer) SyncOrgCLITools(ctx context.Context, orgID string) error {
	builtin, err := LoadBuiltin()
	if err != nil {
		return fmt.Errorf("load builtin manifest: %w", err)
	}
	tools := enabledBuiltinTools(builtin)

	plugins, err := s.store.ListPluginsByKind(ctx, config.PluginKindCLI)
	if err != nil {
		return fmt.Errorf("list cli plugins: %w", err)
	}
	for _, t := range config.CLIToolsFromPlugins(plugins) {
		if !t.Enabled {
			continue
		}
		if !orgToolKeyAllowed(t.Tool) {
			slog.Warn("manifestplugins: skipping org cli tool with disallowed mise backend",
				"org_id", orgID, "tool", t.Name, "key", t.Tool)
			continue
		}
		tools = append(tools, miseToolFromCLITool(t))
	}

	return installScope(ctx, s.stellaHome, scopeForOrg(orgID), tools)
}

// enabledBuiltinTools collects mise tools from every enabled builtin plugin.
func enabledBuiltinTools(m *Manifest) []miseTool {
	var tools []miseTool
	for _, p := range m.Plugins {
		if !p.Enabled {
			continue
		}
		for _, b := range p.Binaries {
			tools = append(tools, miseToolFromBinary(b))
		}
	}
	return tools
}

// miseToolFromCLITool maps an org cli plugin tool to a renderable mise entry.
func miseToolFromCLITool(t config.CLITool) miseTool {
	return miseTool{
		Key:     t.Tool,
		Version: t.Version,
		Options: t.Options,
		Lookup:  t.Name,
	}
}
