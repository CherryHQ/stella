package feishutool

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	"github.com/vaayne/anna/internal/toolspec"
)

var driveInputSchema = mustParseSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list_files", "get_file_meta", "copy_file", "move_file", "delete_file", "create_folder"],
      "description": "The action to perform"
    },
    "file_token": {
      "type": "string",
      "description": "File token (required for copy_file, move_file, delete_file)"
    },
    "folder_token": {
      "type": "string",
      "description": "Folder token for list_files (optional, defaults to root) or target folder for copy_file/create_folder"
    },
    "file_type": {
      "type": "string",
      "enum": ["doc", "sheet", "file", "bitable", "docx", "folder", "mindnote", "slides"],
      "description": "File type (required for copy_file, move_file, delete_file)"
    },
    "name": {
      "type": "string",
      "description": "Name for copy_file (target name) or create_folder"
    },
    "request_docs": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "doc_token": {"type": "string"},
          "doc_type": {"type": "string", "enum": ["doc", "sheet", "file", "bitable", "docx", "folder", "mindnote", "slides"]}
        }
      },
      "description": "Documents to query metadata for get_file_meta (max 50). Example: [{\"doc_token\":\"xxx\",\"doc_type\":\"sheet\"}]"
    },
    "order_by": {
      "type": "string",
      "enum": ["EditedTime", "CreatedTime"],
      "description": "Sort by for list_files"
    },
    "direction": {
      "type": "string",
      "enum": ["ASC", "DESC"],
      "description": "Sort direction for list_files"
    },
    "page_size": {
      "type": "number",
      "description": "Page size for list_files (default 200, max 200)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`)

// DriveTool provides Feishu Drive file management.
type DriveTool struct {
	client *Client
}

// NewDriveTool creates a feishu_drive tool.
func NewDriveTool(client *Client) *DriveTool {
	return &DriveTool{client: client}
}

func (t *DriveTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_drive",
		Description: `Manage Feishu/Lark Drive files. Uses user token when available.

Actions:
- list_files: List files in a folder. Optional: folder_token (defaults to root), order_by, direction, page_size, page_token.
- get_file_meta: Batch query file metadata. Requires request_docs array (max 50). Each item needs doc_token and doc_type.
- copy_file: Copy a file. Requires file_token, name, file_type. Optional: folder_token (target folder).
- move_file: Move a file. Requires file_token, file_type, folder_token (target folder).
- delete_file: Delete a file. Requires file_token, file_type.
- create_folder: Create a folder. Requires name. Optional: folder_token (parent folder, defaults to root).

NOTE: This tool manages Drive files (cloud storage). For reading message attachments, use feishu_im.`,
		InputSchema: driveInputSchema,
	}
}

func (t *DriveTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "list_files":
		return t.listFiles(ctx, args)
	case "get_file_meta":
		return t.getFileMeta(ctx, args)
	case "copy_file":
		return t.copyFile(ctx, args)
	case "move_file":
		return t.moveFile(ctx, args)
	case "delete_file":
		return t.deleteFile(ctx, args)
	case "create_folder":
		return t.createFolder(ctx, args)
	default:
		return "", fmt.Errorf("feishu_drive: unknown action %q", action)
	}
}

func (t *DriveTool) listFiles(ctx context.Context, args map[string]any) (string, error) {
	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkdrive.NewListFileReqBuilder()
		if folder := stringArg(args, "folder_token"); folder != "" {
			builder.FolderToken(folder)
		}
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}
		if ob := stringArg(args, "order_by"); ob != "" {
			builder.OrderBy(ob)
		}
		if dir := stringArg(args, "direction"); dir != "" {
			builder.Direction(dir)
		}

		resp, err := t.client.Lark().Drive.File.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list files: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = paginatedResultMap("files", resp.Data.Files, resp.Data.HasMore, resp.Data.NextPageToken)
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive list_files: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *DriveTool) getFileMeta(ctx context.Context, args map[string]any) (string, error) {
	docs := sliceArg(args, "request_docs")
	if len(docs) == 0 {
		return "", fmt.Errorf("feishu_drive get_file_meta: request_docs is required")
	}

	var requestDocs []*larkdrive.RequestDoc
	for _, item := range docs {
		if m, ok := item.(map[string]any); ok {
			docToken, _ := m["doc_token"].(string)
			docType, _ := m["doc_type"].(string)
			if docToken != "" && docType != "" {
				requestDocs = append(requestDocs, larkdrive.NewRequestDocBuilder().
					DocToken(docToken).
					DocType(docType).
					Build())
			}
		}
	}
	if len(requestDocs) == 0 {
		return "", fmt.Errorf("feishu_drive get_file_meta: no valid docs in request_docs")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Drive.Meta.BatchQuery(ctx,
			larkdrive.NewBatchQueryMetaReqBuilder().
				MetaRequest(larkdrive.NewMetaRequestBuilder().
					RequestDocs(requestDocs).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get file meta: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get file meta: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"metas": resp.Data.Metas}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive get_file_meta: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *DriveTool) copyFile(ctx context.Context, args map[string]any) (string, error) {
	fileToken := stringArg(args, "file_token")
	name := stringArg(args, "name")
	fileType := stringArg(args, "file_type")
	if fileToken == "" || name == "" || fileType == "" {
		return "", fmt.Errorf("feishu_drive copy_file: file_token, name, and file_type are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkdrive.NewCopyFileReqBodyBuilder().
			Name(name).
			Type(fileType)
		if folder := stringArg(args, "folder_token"); folder != "" {
			bodyBuilder.FolderToken(folder)
		}

		resp, err := t.client.Lark().Drive.File.Copy(ctx,
			larkdrive.NewCopyFileReqBuilder().
				FileToken(fileToken).
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("copy file: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("copy file: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"file": resp.Data.File}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive copy_file: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *DriveTool) moveFile(ctx context.Context, args map[string]any) (string, error) {
	fileToken := stringArg(args, "file_token")
	fileType := stringArg(args, "file_type")
	folderToken := stringArg(args, "folder_token")
	if fileToken == "" || fileType == "" || folderToken == "" {
		return "", fmt.Errorf("feishu_drive move_file: file_token, file_type, and folder_token are required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Drive.File.Move(ctx,
			larkdrive.NewMoveFileReqBuilder().
				FileToken(fileToken).
				Body(larkdrive.NewMoveFileReqBodyBuilder().
					Type(fileType).
					FolderToken(folderToken).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("move file: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("move file: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive move_file: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "file_token": fileToken})
}

func (t *DriveTool) deleteFile(ctx context.Context, args map[string]any) (string, error) {
	fileToken := stringArg(args, "file_token")
	fileType := stringArg(args, "file_type")
	if fileToken == "" || fileType == "" {
		return "", fmt.Errorf("feishu_drive delete_file: file_token and file_type are required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Drive.File.Delete(ctx,
			larkdrive.NewDeleteFileReqBuilder().
				FileToken(fileToken).
				Type(fileType).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("delete file: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive delete_file: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "file_token": fileToken})
}

func (t *DriveTool) createFolder(ctx context.Context, args map[string]any) (string, error) {
	name := stringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("feishu_drive create_folder: name is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkdrive.NewCreateFolderFileReqBodyBuilder().
			Name(name)
		if folder := stringArg(args, "folder_token"); folder != "" {
			bodyBuilder.FolderToken(folder)
		}

		resp, err := t.client.Lark().Drive.File.CreateFolder(ctx,
			larkdrive.NewCreateFolderFileReqBuilder().
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create folder: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create folder: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{
			"token": resp.Data.Token,
			"url":   resp.Data.Url,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_drive create_folder: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}
