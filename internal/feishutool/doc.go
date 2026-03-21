package feishutool

import (
	"context"
	"encoding/json"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	"github.com/vaayne/anna/internal/toolspec"
)

var docInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create_doc", "get_doc_content", "get_doc_raw_content"],
      "description": "The action to perform"
    },
    "document_id": {
      "type": "string",
      "description": "Document ID (required for get_doc_content, get_doc_raw_content)"
    },
    "title": {
      "type": "string",
      "description": "Document title (for create_doc)"
    },
    "folder_token": {
      "type": "string",
      "description": "Folder token to create the document in (optional for create_doc)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size for block listing (default 500, max 500)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token for block listing"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// DocTool provides Feishu document (Docx) operations.
type DocTool struct {
	client *Client
}

// NewDocTool creates a feishu_doc tool.
func NewDocTool(client *Client) *DocTool {
	return &DocTool{client: client}
}

func (t *DocTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_doc",
		Description: `Manage Feishu/Lark documents (Docx API). Uses user token when available.

Actions:
- create_doc: Create a new document. Optional: title, folder_token.
- get_doc_content: Get document content as structured blocks (JSON). Requires document_id. Returns block tree with types like paragraph, heading, code, table, etc.
- get_doc_raw_content: Get document content as plain text. Requires document_id. Useful for quick text extraction without block structure.

Feishu documents use a block-based structure. get_doc_content returns the full block tree for programmatic processing. get_doc_raw_content returns plain text for simple reading.`,
		InputSchema: docInputSchema,
	}
}

func (t *DocTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "create_doc":
		return t.createDoc(ctx, args)
	case "get_doc_content":
		return t.getDocContent(ctx, args)
	case "get_doc_raw_content":
		return t.getDocRawContent(ctx, args)
	default:
		return "", fmt.Errorf("feishu_doc: unknown action %q", action)
	}
}

func (t *DocTool) createDoc(ctx context.Context, args map[string]any) (string, error) {
	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkdocx.NewCreateDocumentReqBuilder()
		bodyBuilder := larkdocx.NewCreateDocumentReqBodyBuilder()
		if title := stringArg(args, "title"); title != "" {
			bodyBuilder.Title(title)
		}
		if folder := stringArg(args, "folder_token"); folder != "" {
			bodyBuilder.FolderToken(folder)
		}
		builder.Body(bodyBuilder.Build())

		resp, err := t.client.Lark().Docx.Document.Create(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("create doc: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create doc: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"document": resp.Data.Document}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_doc create_doc: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *DocTool) getDocContent(ctx context.Context, args map[string]any) (string, error) {
	docID := stringArg(args, "document_id")
	if docID == "" {
		return "", fmt.Errorf("feishu_doc get_doc_content: document_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkdocx.NewListDocumentBlockReqBuilder().
			DocumentId(docID)
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Docx.DocumentBlock.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("get doc content: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get doc content: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"blocks":     resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_doc get_doc_content: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *DocTool) getDocRawContent(ctx context.Context, args map[string]any) (string, error) {
	docID := stringArg(args, "document_id")
	if docID == "" {
		return "", fmt.Errorf("feishu_doc get_doc_raw_content: document_id is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Docx.Document.RawContent(ctx,
			larkdocx.NewRawContentDocumentReqBuilder().
				DocumentId(docID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get doc raw content: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get doc raw content: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{
			"content": resp.Data.Content,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_doc get_doc_raw_content: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}
