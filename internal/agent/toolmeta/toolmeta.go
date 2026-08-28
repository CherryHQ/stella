// Package toolmeta describes generated model-facing tools: the name, the
// family it belongs to, and the exact input schema bound to one action.
//
// It deliberately depends on nothing but pkg/tools. The runner, the delegate
// preset resolver and every generated tool_gen.go import it, so any dependency
// on internal/agent would close an import cycle.
package toolmeta

import (
	"sort"
	"strings"
	"sync/atomic"

	"github.com/CherryHQ/stella/pkg/tools"
)

// ActionTool is one generated tool: an exact schema bound to one action.
// Family and Resource are carried, not inferred — a plugin free to call itself
// "goal_helper" must never be mistaken for a member of the goal family.
type ActionTool struct {
	// Name is the model-facing tool name, e.g. "recally_feed_add".
	Name string
	// Family groups tools that share a domain, e.g. "recally".
	Family string
	// Resource is the family's sub-resource, e.g. "feed". Empty when the
	// family owns the action directly.
	Resource string
	// Action is the dispatch key inside the family, e.g. "feed_add".
	Action string
	// Description is the declared model-facing description. Operation-backed
	// tools leave it empty and pass prose in from their hand-written adapter.
	Description string
	// InputSchemaJSON is the generated JSON Schema for this tool's arguments.
	InputSchemaJSON string
}

// InputSchema decodes the generated schema. It panics on malformed JSON
// because the input is a compile-time constant produced by toolgen.
func (a ActionTool) InputSchema() map[string]any {
	return tools.MustInputSchema(a.InputSchemaJSON)
}

// Definition builds the model-facing definition. description wins when
// non-empty: operation-backed tools keep their prose in the hand-written
// adapter next to the handler, declaration-backed tools carry it in the spec.
func (a ActionTool) Definition(description string) tools.Definition {
	if description == "" {
		description = a.Description
	}
	return tools.Definition{Name: a.Name, Description: description, InputSchema: a.InputSchema()}
}

// Registry is a lookup over the generated tools a build actually registered.
// Family answers from this metadata rather than by splitting a name on "_",
// which would misread every plugin and MCP tool that happens to contain one.
type Registry struct {
	byName map[string]ActionTool
}

// NewRegistry indexes tools by name. A duplicate name is a build-time bug in
// toolgen's uniqueness check; last one wins here rather than panicking at
// startup.
func NewRegistry(all ...ActionTool) *Registry {
	byName := make(map[string]ActionTool, len(all))
	for _, tool := range all {
		byName[tool.Name] = tool
	}
	return &Registry{byName: byName}
}

// Lookup returns the declaration for a tool name.
func (r *Registry) Lookup(name string) (ActionTool, bool) {
	if r == nil {
		return ActionTool{}, false
	}
	tool, ok := r.byName[name]
	return tool, ok
}

// Family returns the family a registered tool belongs to, or "" when the name
// is not a generated tool (plugins, MCP, hand-written core tools).
func (r *Registry) Family(name string) string {
	tool, ok := r.Lookup(name)
	if !ok {
		return ""
	}
	return tool.Family
}

// Names lists every registered tool name in a stable order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Match reports whether a selector from a delegate preset's tools: list or from
// excluded_tools refers to this tool. A selector is either an exact tool name
// or a family name; family membership comes from the declaration, so a plugin
// named "goal_helper" is never swept in by the family "goal".
func Match(selector string, tool ActionTool) bool {
	if selector == "" {
		return false
	}
	if selector == tool.Name || selector == tool.Family {
		return true
	}
	if canonical, ok := legacyNames[selector]; ok {
		return canonical == tool.Name || canonical == tool.Family
	}
	return false
}

// MatchAny reports whether any selector matches the tool.
func MatchAny(selectors []string, tool ActionTool) bool {
	for _, selector := range selectors {
		if Match(selector, tool) {
			return true
		}
	}
	return false
}

// defaultRegistry is the set of generated tools this build registered. The set
// is fixed at compile time, but toolmeta cannot read it directly: every
// tool_gen.go imports this package, so importing them back would close a cycle.
// cmd/stellad installs it once during startup, before any runner exists, and
// nothing writes it again.
//
// Until it is installed, MatchName degrades to exact-name matching, which is
// what the call sites did before family selectors existed. That is the reason
// this is a nil-tolerant pointer rather than a required constructor argument
// threaded through the runner, the service and the delegate tool.
var defaultRegistry atomic.Pointer[Registry]

// SetDefaultRegistry installs the build's generated-tool registry for name-only
// call sites. Call it once, from process startup.
func SetDefaultRegistry(reg *Registry) { defaultRegistry.Store(reg) }

// DefaultRegistry returns the installed registry, or nil.
func DefaultRegistry() *Registry { return defaultRegistry.Load() }

// MatchName is Match for a call site that only has a tool name: the runner's
// excluded_tools filter and the delegate preset whitelist both work off
// tools.Definition, which carries no family.
//
// A name this build did not generate matches only itself. A plugin called
// "goal_helper" is not swept up by the family selector "goal", and a legacy
// name only redirects when the registry knows what replaced it.
func MatchName(selector, name string) bool {
	if selector == "" {
		return false
	}
	if selector == name {
		return true
	}
	tool, ok := DefaultRegistry().Lookup(name)
	if !ok {
		return false
	}
	return Match(selector, tool)
}

// MatchAnyName reports whether any selector matches the named tool.
func MatchAnyName(selectors []string, name string) bool {
	for _, selector := range selectors {
		if MatchName(selector, name) {
			return true
		}
	}
	return false
}

// SelectsNothing reports whether a selector matches no registered tool, so a
// caller can warn about a stale entry in a user-written preset instead of
// silently hiding every tool.
func SelectsNothing(selector string, names []string) bool {
	for _, name := range names {
		if MatchName(selector, name) {
			return false
		}
	}
	return true
}

// legacyNames maps a tool name retired by a rename or a split to its
// replacement, so a selector written against the previous release keeps
// selecting the same capability for one deprecation release.
//
// The seven retired union names need no entry: a union name was always its own
// family name, so "scheduler" still selects the family through Match. Only the
// four exact renames inside recally do, and they leave with the contract
// migration in the release after this one (see rules/agent-tools.md §10).
var legacyNames = map[string]string{
	"recally_save_article":  "recally_article_save",
	"recally_list_articles": "recally_article_list",
	"recally_get_article":   "recally_article_get",
	"recally_digest":        "recally_digest_get",
}

// handWritten is the closed list of model-facing tools that legitimately have
// no declaration: core sandbox tools, the plugin and MCP surfaces, and the two
// protocols whose schema is not a REST contract. Everything else goes through
// toolgen, so its schema, its name and its Go input type stay in one place.
//
// Adding an entry is a design decision, not a convenience: it means the tool
// has no HTTP operation and no declaration file that could describe it. Say why
// in the PR (see rules/agent-tools.md §2).
var handWritten = map[string]bool{
	"bash":         true, // core sandbox
	"view_image":   true, // core sandbox
	"webfetch":     true, // plugin
	"notify":       true, // channel dispatcher, not a REST resource
	"goal_control": true, // attempt protocol, one name with three schemas
	"code":         true, // meta-tool over the other tools
}

// handWrittenPrefixes cover the two families whose names are not fixed at build
// time: MCP tools come from a remote server, and the library tools are
// hand-written single-operation tools.
var handWrittenPrefixes = []string{"mcp__", "library_"}

// pendingSplit are hand-written today only because their split has not landed
// yet: memory, skills and session are still unions with an `action` enum. They
// are separate from handWritten so that "hand-written on purpose" and "not
// converted yet" never blur together — each entry leaves this map in the PR
// that converts it, and the map is meant to reach empty.
var pendingSplit = map[string]bool{
	"memory":  true, // internal/memory/tool.go
	"skills":  true, // internal/skills/tool.go
	"session": true, // internal/agent/session/access/tool.go
}

// HandWritten reports whether a tool name is an accepted exception to
// "every model-facing tool is generated" — permanently, or until its split
// lands.
func HandWritten(name string) bool {
	if handWritten[name] || pendingSplit[name] {
		return true
	}
	for _, prefix := range handWrittenPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
