package feishutool

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkwiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
	"github.com/vaayne/anna/internal/toolspec"
)

var wikiInputSchema = mustParseSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list_spaces", "get_space", "create_space_node", "list_space_nodes", "get_node", "move_node", "copy_node"],
      "description": "The action to perform"
    },
    "space_id": {
      "type": "string",
      "description": "Wiki space ID (required for get_space, create_space_node, list_space_nodes, move_node, copy_node)"
    },
    "node_token": {
      "type": "string",
      "description": "Node token (required for get_node, move_node, copy_node)"
    },
    "parent_node_token": {
      "type": "string",
      "description": "Parent node token (optional for list_space_nodes to list children, for create_space_node)"
    },
    "obj_type": {
      "type": "string",
      "enum": ["doc", "sheet", "mindnote", "bitable", "file", "docx", "slides"],
      "description": "Object type for create_space_node (required) or get_node (optional, default 'wiki')"
    },
    "node_type": {
      "type": "string",
      "enum": ["origin", "shortcut"],
      "description": "Node type for create_space_node: origin (new doc) or shortcut (link to existing)"
    },
    "title": {
      "type": "string",
      "description": "Title for create_space_node or copy_node"
    },
    "target_parent_token": {
      "type": "string",
      "description": "Target parent node token for move_node or copy_node"
    },
    "target_space_id": {
      "type": "string",
      "description": "Target space ID for copy_node (optional, defaults to same space)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size (default 50)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`)

// WikiTool provides Feishu wiki space and node management.
type WikiTool struct {
	client *Client
}

// NewWikiTool creates a feishu_wiki tool.
func NewWikiTool(client *Client) *WikiTool {
	return &WikiTool{client: client}
}

func (t *WikiTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_wiki",
		Description: `Manage Feishu/Lark wiki spaces and nodes. Uses user token when available.

A wiki space contains hierarchical document nodes. Nodes can be docs, sheets, bitables, etc.

Actions:
- list_spaces: List all wiki spaces accessible to the user.
- get_space: Get wiki space details. Requires space_id.
- create_space_node: Create a node in a wiki space. Requires space_id, obj_type (docx/sheet/bitable/etc), node_type (origin/shortcut). Optional: parent_node_token, title.
- list_space_nodes: List nodes in a wiki space. Requires space_id. Optional: parent_node_token (for children of a specific node).
- get_node: Get node details (resolves wiki token to actual document token). Requires node_token. Optional: obj_type.
- move_node: Move a node within a wiki space. Requires space_id, node_token. Optional: target_parent_token.
- copy_node: Copy a node. Requires space_id, node_token. Optional: target_space_id, target_parent_token, title.

node_token is the wiki node identifier. Use get_node to resolve it to the actual document obj_token for use with other tools (feishu_doc, feishu_sheets, etc).`,
		InputSchema: wikiInputSchema,
	}
}

func (t *WikiTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "list_spaces":
		return t.listSpaces(ctx, args)
	case "get_space":
		return t.getSpace(ctx, args)
	case "create_space_node":
		return t.createSpaceNode(ctx, args)
	case "list_space_nodes":
		return t.listSpaceNodes(ctx, args)
	case "get_node":
		return t.getNode(ctx, args)
	case "move_node":
		return t.moveNode(ctx, args)
	case "copy_node":
		return t.copyNode(ctx, args)
	default:
		return "", fmt.Errorf("feishu_wiki: unknown action %q", action)
	}
}

func (t *WikiTool) listSpaces(ctx context.Context, args map[string]any) (string, error) {
	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkwiki.NewListSpaceReqBuilder()
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Wiki.Space.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list spaces: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list spaces: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = paginatedResultMap("spaces", resp.Data.Items, resp.Data.HasMore, resp.Data.PageToken)
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki list_spaces: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) getSpace(ctx context.Context, args map[string]any) (string, error) {
	spaceID := stringArg(args, "space_id")
	if spaceID == "" {
		return "", fmt.Errorf("feishu_wiki get_space: space_id is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Wiki.Space.Get(ctx,
			larkwiki.NewGetSpaceReqBuilder().
				SpaceId(spaceID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get space: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get space: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"space": resp.Data.Space}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki get_space: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) createSpaceNode(ctx context.Context, args map[string]any) (string, error) {
	spaceID := stringArg(args, "space_id")
	objType := stringArg(args, "obj_type")
	nodeType := stringArg(args, "node_type")
	if spaceID == "" || objType == "" || nodeType == "" {
		return "", fmt.Errorf("feishu_wiki create_space_node: space_id, obj_type, and node_type are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		nodeBuilder := larkwiki.NewNodeBuilder().
			ObjType(objType).
			NodeType(nodeType)
		if parent := stringArg(args, "parent_node_token"); parent != "" {
			nodeBuilder.ParentNodeToken(parent)
		}
		if title := stringArg(args, "title"); title != "" {
			nodeBuilder.Title(title)
		}

		resp, err := t.client.Lark().Wiki.SpaceNode.Create(ctx,
			larkwiki.NewCreateSpaceNodeReqBuilder().
				SpaceId(spaceID).
				Node(nodeBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create space node: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create space node: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"node": resp.Data.Node}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki create_space_node: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) listSpaceNodes(ctx context.Context, args map[string]any) (string, error) {
	spaceID := stringArg(args, "space_id")
	if spaceID == "" {
		return "", fmt.Errorf("feishu_wiki list_space_nodes: space_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkwiki.NewListSpaceNodeReqBuilder().
			SpaceId(spaceID)
		if parent := stringArg(args, "parent_node_token"); parent != "" {
			builder.ParentNodeToken(parent)
		}
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Wiki.SpaceNode.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list space nodes: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list space nodes: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = paginatedResultMap("nodes", resp.Data.Items, resp.Data.HasMore, resp.Data.PageToken)
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki list_space_nodes: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) getNode(ctx context.Context, args map[string]any) (string, error) {
	token := stringArg(args, "node_token")
	if token == "" {
		return "", fmt.Errorf("feishu_wiki get_node: node_token is required")
	}
	objType := stringArg(args, "obj_type")
	if objType == "" {
		objType = "wiki"
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Wiki.Space.GetNode(ctx,
			larkwiki.NewGetNodeSpaceReqBuilder().
				Token(token).
				ObjType(objType).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get node: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get node: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"node": resp.Data.Node}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki get_node: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) moveNode(ctx context.Context, args map[string]any) (string, error) {
	spaceID := stringArg(args, "space_id")
	nodeToken := stringArg(args, "node_token")
	if spaceID == "" || nodeToken == "" {
		return "", fmt.Errorf("feishu_wiki move_node: space_id and node_token are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkwiki.NewMoveSpaceNodeReqBuilder().
			SpaceId(spaceID).
			NodeToken(nodeToken)
		if target := stringArg(args, "target_parent_token"); target != "" {
			builder.Body(larkwiki.NewMoveSpaceNodeReqBodyBuilder().
				TargetParentToken(target).
				Build())
		}

		resp, err := t.client.Lark().Wiki.SpaceNode.Move(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("move node: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("move node: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"node": resp.Data.Node}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki move_node: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *WikiTool) copyNode(ctx context.Context, args map[string]any) (string, error) {
	spaceID := stringArg(args, "space_id")
	nodeToken := stringArg(args, "node_token")
	if spaceID == "" || nodeToken == "" {
		return "", fmt.Errorf("feishu_wiki copy_node: space_id and node_token are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkwiki.NewCopySpaceNodeReqBodyBuilder()
		if target := stringArg(args, "target_space_id"); target != "" {
			bodyBuilder.TargetSpaceId(target)
		}
		if target := stringArg(args, "target_parent_token"); target != "" {
			bodyBuilder.TargetParentToken(target)
		}
		if title := stringArg(args, "title"); title != "" {
			bodyBuilder.Title(title)
		}

		resp, err := t.client.Lark().Wiki.SpaceNode.Copy(ctx,
			larkwiki.NewCopySpaceNodeReqBuilder().
				SpaceId(spaceID).
				NodeToken(nodeToken).
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("copy node: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("copy node: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"node": resp.Data.Node}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_wiki copy_node: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}
