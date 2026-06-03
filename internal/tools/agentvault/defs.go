package agentvault

import "github.com/CherryHQ/stella/pkg/tools"

// Identity (the acting user) comes from context, so it is never a schema field.

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

func listDef() tools.Definition {
	return tools.Definition{
		Name:        "vault_list",
		Description: "List the names of your stored secrets (metadata only — values are never returned).",
		InputSchema: objectSchema(map[string]any{}),
	}
}

func setDef() tools.Definition {
	return tools.Definition{
		Name: "vault_set",
		Description: "Store (or overwrite) an encrypted secret by name so the agent can use it at runtime. " +
			"The value is injected into sandbox env, never read back into the conversation.",
		InputSchema: objectSchema(map[string]any{
			"name":  strProp("Secret name (env-var style, e.g. GITHUB_TOKEN)."),
			"value": strProp("Secret value to encrypt and store."),
		}, "name", "value"),
	}
}

func deleteDef() tools.Definition {
	return tools.Definition{
		Name:        "vault_delete",
		Description: "Delete one of your stored secrets by name.",
		InputSchema: objectSchema(map[string]any{
			"name": strProp("Secret name to delete."),
		}, "name"),
	}
}
