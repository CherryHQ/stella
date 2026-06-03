package agentshare

import "github.com/CherryHQ/stella/pkg/tools"

// Identity (the acting user and agent) comes from context, so it is never a
// schema field.

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

func expiresProp() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "Link lifetime: 1h, 1d, 7d (default), or never.",
		"enum":        []any{"1h", "1d", "7d", "never"},
	}
}

func artifactDef() tools.Definition {
	return tools.Definition{
		Name: "share_artifact",
		Description: "Publish a file from your workspace as a public, view-only link. " +
			"Supports HTML, Markdown, PDF, SVG, and common image types.",
		InputSchema: objectSchema(map[string]any{
			"path":       strProp("Workspace-relative path to the file (required)."),
			"expires_in": expiresProp(),
		}, "path"),
	}
}

func articleDef() tools.Definition {
	return tools.Definition{
		Name:        "share_article",
		Description: "Publish one of your saved articles as a public, view-only web page.",
		InputSchema: objectSchema(map[string]any{
			"article_id": strProp("ID of the saved article to share (required)."),
			"expires_in": expiresProp(),
		}, "article_id"),
	}
}
