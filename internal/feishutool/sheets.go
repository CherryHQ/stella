package feishutool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larksheets "github.com/larksuite/oapi-sdk-go/v3/service/sheets/v3"
	"github.com/vaayne/anna/internal/toolspec"
)

var sheetsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create_spreadsheet", "get_spreadsheet", "list_sheets", "read_range", "write_range"],
      "description": "The action to perform"
    },
    "spreadsheet_token": {
      "type": "string",
      "description": "Spreadsheet token (required for all actions except create_spreadsheet)"
    },
    "title": {
      "type": "string",
      "description": "Spreadsheet title (for create_spreadsheet)"
    },
    "folder_token": {
      "type": "string",
      "description": "Folder token for create_spreadsheet (optional, defaults to root)"
    },
    "range": {
      "type": "string",
      "description": "Cell range for read/write, e.g. 'sheetId!A1:D10' or just 'sheetId' for entire sheet. sheetId from list_sheets action."
    },
    "values": {
      "type": "array",
      "items": {"type": "array", "items": {}},
      "description": "2D array for write_range. Each inner array is a row. Example: [[\"Name\",\"Age\"],[\"Alice\",25]]"
    },
    "value_render_option": {
      "type": "string",
      "enum": ["ToString", "FormattedValue", "Formula", "UnformattedValue"],
      "description": "How to render cell values for read_range (default: ToString)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// SheetsTool provides Feishu spreadsheet operations.
type SheetsTool struct {
	client *Client
}

// NewSheetsTool creates a feishu_sheets tool.
func NewSheetsTool(client *Client) *SheetsTool {
	return &SheetsTool{client: client}
}

func (t *SheetsTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_sheets",
		Description: `Manage Feishu/Lark spreadsheets. Uses user token when available.

Spreadsheets (Sheets) are like Excel/Google Sheets. Different from Bitable (which is like Airtable).

Actions:
- create_spreadsheet: Create a new spreadsheet. Optional: title, folder_token.
- get_spreadsheet: Get spreadsheet metadata. Requires spreadsheet_token.
- list_sheets: List all worksheets in a spreadsheet. Requires spreadsheet_token. Returns sheet_id, title, row/column counts.
- read_range: Read cell data from a range. Requires spreadsheet_token and range (format: 'sheetId!A1:D10' or 'sheetId'). Optional: value_render_option.
- write_range: Write data to a range. Requires spreadsheet_token, range, and values (2D array). Overwrites existing data in the range.

Get sheet_id from list_sheets, then use it in range like 'abc123!A1:D10'.`,
		InputSchema: sheetsInputSchema,
	}
}

func (t *SheetsTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "create_spreadsheet":
		return t.createSpreadsheet(ctx, args)
	case "get_spreadsheet":
		return t.getSpreadsheet(ctx, args)
	case "list_sheets":
		return t.listSheets(ctx, args)
	case "read_range":
		return t.readRange(ctx, args)
	case "write_range":
		return t.writeRange(ctx, args)
	default:
		return "", fmt.Errorf("feishu_sheets: unknown action %q", action)
	}
}

func (t *SheetsTool) createSpreadsheet(ctx context.Context, args map[string]any) (string, error) {
	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		ssBuilder := larksheets.NewSpreadsheetBuilder()
		if title := stringArg(args, "title"); title != "" {
			ssBuilder.Title(title)
		}
		if folder := stringArg(args, "folder_token"); folder != "" {
			ssBuilder.FolderToken(folder)
		}

		resp, err := t.client.Lark().Sheets.Spreadsheet.Create(ctx,
			larksheets.NewCreateSpreadsheetReqBuilder().
				Spreadsheet(ssBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create spreadsheet: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create spreadsheet: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"spreadsheet": resp.Data.Spreadsheet}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_sheets create_spreadsheet: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *SheetsTool) getSpreadsheet(ctx context.Context, args map[string]any) (string, error) {
	token := stringArg(args, "spreadsheet_token")
	if token == "" {
		return "", fmt.Errorf("feishu_sheets get_spreadsheet: spreadsheet_token is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Sheets.Spreadsheet.Get(ctx,
			larksheets.NewGetSpreadsheetReqBuilder().
				SpreadsheetToken(token).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get spreadsheet: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get spreadsheet: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"spreadsheet": resp.Data.Spreadsheet}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_sheets get_spreadsheet: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *SheetsTool) listSheets(ctx context.Context, args map[string]any) (string, error) {
	token := stringArg(args, "spreadsheet_token")
	if token == "" {
		return "", fmt.Errorf("feishu_sheets list_sheets: spreadsheet_token is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Sheets.SpreadsheetSheet.Query(ctx,
			larksheets.NewQuerySpreadsheetSheetReqBuilder().
				SpreadsheetToken(token).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("list sheets: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list sheets: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"sheets": resp.Data.Sheets}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_sheets list_sheets: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *SheetsTool) readRange(ctx context.Context, args map[string]any) (string, error) {
	token := stringArg(args, "spreadsheet_token")
	rangeStr := stringArg(args, "range")
	if token == "" || rangeStr == "" {
		return "", fmt.Errorf("feishu_sheets read_range: spreadsheet_token and range are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		// Sheets v2 API for reading cell values.
		apiPath := fmt.Sprintf("/open-apis/sheets/v2/spreadsheets/%s/values/%s",
			url.PathEscape(token), url.PathEscape(rangeStr))

		vro := stringArg(args, "value_render_option")
		if vro == "" {
			vro = "ToString"
		}
		apiPath += "?valueRenderOption=" + url.QueryEscape(vro) + "&dateTimeRenderOption=FormattedString"

		resp, err := t.client.Lark().Get(ctx, apiPath, nil, larkcore.AccessTokenTypeTenant, opts...)
		if err != nil {
			return fmt.Errorf("read range: %w", err)
		}

		var respData struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				ValueRange struct {
					Range  string  `json:"range"`
					Values [][]any `json:"values"`
				} `json:"valueRange"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.RawBody, &respData); err != nil {
			return fmt.Errorf("read range: decode response: %w", err)
		}
		if respData.Code != 0 {
			return fmt.Errorf("read range: %s", FormatLarkError(respData.Code, respData.Msg))
		}

		result = map[string]any{
			"range":  respData.Data.ValueRange.Range,
			"values": respData.Data.ValueRange.Values,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_sheets read_range: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *SheetsTool) writeRange(ctx context.Context, args map[string]any) (string, error) {
	token := stringArg(args, "spreadsheet_token")
	rangeStr := stringArg(args, "range")
	values := sliceArg(args, "values")
	if token == "" || rangeStr == "" {
		return "", fmt.Errorf("feishu_sheets write_range: spreadsheet_token and range are required")
	}
	if len(values) == 0 {
		return "", fmt.Errorf("feishu_sheets write_range: values is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		// Sheets v2 API for writing cell values.
		apiPath := fmt.Sprintf("/open-apis/sheets/v2/spreadsheets/%s/values",
			url.PathEscape(token))

		body := map[string]any{
			"valueRange": map[string]any{
				"range":  rangeStr,
				"values": values,
			},
		}

		resp, err := t.client.Lark().Put(ctx, apiPath, body, larkcore.AccessTokenTypeTenant, opts...)
		if err != nil {
			return fmt.Errorf("write range: %w", err)
		}

		var respData struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				UpdatedRange   string `json:"updatedRange"`
				UpdatedRows    int    `json:"updatedRows"`
				UpdatedColumns int    `json:"updatedColumns"`
				UpdatedCells   int    `json:"updatedCells"`
				Revision       int    `json:"revision"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.RawBody, &respData); err != nil {
			return fmt.Errorf("write range: decode response: %w", err)
		}
		if respData.Code != 0 {
			return fmt.Errorf("write range: %s", FormatLarkError(respData.Code, respData.Msg))
		}

		result = map[string]any{
			"updated_range":   respData.Data.UpdatedRange,
			"updated_rows":    respData.Data.UpdatedRows,
			"updated_columns": respData.Data.UpdatedColumns,
			"updated_cells":   respData.Data.UpdatedCells,
			"revision":        respData.Data.Revision,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_sheets write_range: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}
