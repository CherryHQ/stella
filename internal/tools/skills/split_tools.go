package skills

import (
	"context"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewSplitTools exposes the high-frequency skill operations as discrete native
// tools (skill_search/skill_list/skill_install) instead of the action-enum
// `skills` tool. Identity (user, agent) is read from context by the underlying
// *Tool methods; per-session paths (agentRoot/projectRoot/userSkillsDir) are
// bound at construction by the caller. Returns nil when the store is missing so
// callers can append unconditionally.
func NewSplitTools(store pkgplugins.SkillStore, stellaHome, agentRoot, projectRoot, userSkillsDir string) []tools.Tool {
	if store == nil {
		return nil
	}
	t := NewTool(store, stellaHome, agentRoot, projectRoot, userSkillsDir)
	return []tools.Tool{
		splitTool{searchDef(), t.search},
		splitTool{listDef(), func(ctx context.Context, _ map[string]any) (string, error) { return t.list(ctx) }},
		splitTool{installDef(), t.install},
	}
}

func searchDef() tools.Definition {
	return tools.Definition{
		Name:        "skill_search",
		Description: "Search the skill ecosystem (clawhub.ai and skills.sh) for installable skills. Returns provider/name/description/source; install with skill_install using a returned source.",
		InputSchema: objectSchema(map[string]any{
			"query": strProp("Search query (required)."),
			"limit": intProp("Max results to return (default 10)."),
		}, "query"),
	}
}

func listDef() tools.Definition {
	return tools.Definition{
		Name:        "skill_list",
		Description: "List installed skills visible to the current agent (user, agent, and read-only project scopes).",
		InputSchema: objectSchema(map[string]any{}),
	}
}

func installDef() tools.Definition {
	return tools.Definition{
		Name: "skill_install",
		Description: "Install a skill from a source returned by skill_search (e.g. 'clawhub:<slug>', 'owner/repo@skill-name'). " +
			"Defaults to user scope; set scope=agent to install into the current agent.",
		InputSchema: objectSchema(map[string]any{
			"source": strProp("Skill source to install (required)."),
			"scope":  enumProp("Writable scope: user (default) or agent.", "user", "agent"),
		}, "source"),
	}
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func enumProp(desc string, vals ...string) map[string]any {
	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = v
	}
	return map[string]any{"type": "string", "description": desc, "enum": enum}
}

type splitTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t splitTool) Definition() tools.Definition { return t.def }
func (t splitTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}
