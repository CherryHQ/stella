package agentoauth

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

func providersDef() tools.Definition {
	return tools.Definition{
		Name:        "oauth_providers",
		Description: "List the OAuth providers available to connect and your current connection status for each.",
		InputSchema: objectSchema(map[string]any{}),
	}
}

func statusDef() tools.Definition {
	return tools.Definition{
		Name:        "oauth_status",
		Description: "Show your connection status for a single OAuth provider.",
		InputSchema: objectSchema(map[string]any{
			"provider": strProp("Provider name (required)."),
		}, "provider"),
	}
}

func connectDef() tools.Definition {
	return tools.Definition{
		Name: "oauth_connect",
		Description: "Start connecting an OAuth provider. Returns a verification URL (and code, if any) immediately for the user to authorize. " +
			"You'll be notified once the connection completes — do not poll.",
		InputSchema: objectSchema(map[string]any{
			"provider": strProp("Provider name to connect (required)."),
		}, "provider"),
	}
}

func disconnectDef() tools.Definition {
	return tools.Definition{
		Name:        "oauth_disconnect",
		Description: "Disconnect an OAuth provider, deleting your stored credentials for it.",
		InputSchema: objectSchema(map[string]any{
			"provider": strProp("Provider name to disconnect (required)."),
		}, "provider"),
	}
}
