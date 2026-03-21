package feishutool

import (
	"context"
	"encoding/json"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkbitable "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
	"github.com/vaayne/anna/internal/toolspec"
)

const maxBatchSize = 500

var bitableInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create_app", "list_tables", "create_table", "list_records", "create_record", "update_record", "delete_record", "batch_create_records", "batch_update_records", "batch_delete_records", "list_fields", "create_field"],
      "description": "The action to perform"
    },
    "app_token": {
      "type": "string",
      "description": "Bitable app token (required for most actions except create_app)"
    },
    "table_id": {
      "type": "string",
      "description": "Table ID (required for record/field operations)"
    },
    "record_id": {
      "type": "string",
      "description": "Record ID (required for update_record, delete_record)"
    },
    "name": {
      "type": "string",
      "description": "Name/title for create_app or create_table"
    },
    "fields": {
      "type": "object",
      "additionalProperties": true,
      "description": "Record fields for create/update. Keys are field names, values depend on field type: text=string, number=number, select=string, multi-select=string[], date=number(ms timestamp), checkbox=boolean, person=[{id:'ou_xxx'}]"
    },
    "records": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "record_id": {"type": "string"},
          "fields": {"type": "object", "additionalProperties": true}
        }
      },
      "description": "Records array for batch operations (max 500)"
    },
    "record_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Record IDs for batch_delete_records (max 500)"
    },
    "view_id": {
      "type": "string",
      "description": "View ID for list_records (optional, improves performance)"
    },
    "field_names": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Field names to return for list_records (optional, returns all if omitted)"
    },
    "filter": {
      "type": "object",
      "properties": {
        "conjunction": {"type": "string", "enum": ["and", "or"]},
        "conditions": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "field_name": {"type": "string"},
              "operator": {"type": "string", "enum": ["is", "isNot", "contains", "doesNotContain", "isEmpty", "isNotEmpty", "isGreater", "isGreaterEqual", "isLess", "isLessEqual"]},
              "value": {"type": "array", "items": {"type": "string"}}
            }
          }
        }
      },
      "description": "Filter for list_records. Example: {conjunction:'and', conditions:[{field_name:'Status', operator:'is', value:['Done']}]}"
    },
    "sort": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "field_name": {"type": "string"},
          "desc": {"type": "boolean"}
        }
      },
      "description": "Sort rules for list_records"
    },
    "automatic_fields": {
      "type": "boolean",
      "description": "Include auto fields (created_time, last_modified_time, etc.) in list_records"
    },
    "field_type": {
      "type": "number",
      "description": "Field type for create_field. 1=Text, 2=Number, 3=Select, 4=MultiSelect, 5=Date, 7=Checkbox, 11=Person, 13=Phone, 15=URL, 17=Attachment, 18=Link, 20=Formula, 21=DuplexLink, 22=Location, 23=GroupChat, 1001=CreatedTime, 1002=ModifiedTime, 1003=CreatedBy, 1004=ModifiedBy, 1005=AutoNumber"
    },
    "field_property": {
      "type": "object",
      "additionalProperties": true,
      "description": "Field property configuration for create_field (type-specific)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size (default 50, max 500)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// BitableTool provides Feishu Bitable (multidimensional spreadsheet) management.
type BitableTool struct {
	client *Client
}

// NewBitableTool creates a feishu_bitable tool.
func NewBitableTool(client *Client) *BitableTool {
	return &BitableTool{client: client}
}

func (t *BitableTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_bitable",
		Description: `Manage Feishu/Lark Bitable (multidimensional spreadsheets). Uses user token when available.

Actions:
- create_app: Create a new Bitable app. Requires name.
- list_tables: List tables in a Bitable app. Requires app_token.
- create_table: Create a table. Requires app_token and name.
- list_records: List/search records (uses Search API). Requires app_token and table_id. Optional: view_id, field_names, filter, sort, automatic_fields, page_size, page_token.
- create_record: Create a single record. Requires app_token, table_id, and fields object.
- update_record: Update a record. Requires app_token, table_id, record_id, and fields object.
- delete_record: Delete a record. Requires app_token, table_id, and record_id.
- batch_create_records: Create multiple records (max 500). Requires app_token, table_id, and records array with fields.
- batch_update_records: Update multiple records (max 500). Requires app_token, table_id, and records array with record_id and fields.
- batch_delete_records: Delete multiple records (max 500). Requires app_token, table_id, and record_ids array.
- list_fields: List fields (columns) of a table. Requires app_token and table_id.
- create_field: Create a field. Requires app_token, table_id, name, and field_type.

Field types for records: text=string, number=number, select=string, multi-select=string[], date=number(ms timestamp), checkbox=boolean, person=[{id:'ou_xxx'}], attachment=[{file_token:'xxx'}].`,
		InputSchema: bitableInputSchema,
	}
}

func (t *BitableTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "create_app":
		return t.createApp(ctx, args)
	case "list_tables":
		return t.listTables(ctx, args)
	case "create_table":
		return t.createTable(ctx, args)
	case "list_records":
		return t.listRecords(ctx, args)
	case "create_record":
		return t.createRecord(ctx, args)
	case "update_record":
		return t.updateRecord(ctx, args)
	case "delete_record":
		return t.deleteRecord(ctx, args)
	case "batch_create_records":
		return t.batchCreateRecords(ctx, args)
	case "batch_update_records":
		return t.batchUpdateRecords(ctx, args)
	case "batch_delete_records":
		return t.batchDeleteRecords(ctx, args)
	case "list_fields":
		return t.listFields(ctx, args)
	case "create_field":
		return t.createField(ctx, args)
	default:
		return "", fmt.Errorf("feishu_bitable: unknown action %q", action)
	}
}

func (t *BitableTool) createApp(ctx context.Context, args map[string]any) (string, error) {
	name := stringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("feishu_bitable create_app: name is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.App.Create(ctx,
			larkbitable.NewCreateAppReqBuilder().
				ReqApp(larkbitable.NewReqAppBuilder().Name(name).Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create app: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create app: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"app": resp.Data.App}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable create_app: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) listTables(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	if appToken == "" {
		return "", fmt.Errorf("feishu_bitable list_tables: app_token is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkbitable.NewListAppTableReqBuilder().AppToken(appToken)
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Bitable.AppTable.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list tables: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list tables: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"tables":     resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable list_tables: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) createTable(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	name := stringArg(args, "name")
	if appToken == "" || name == "" {
		return "", fmt.Errorf("feishu_bitable create_table: app_token and name are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTable.Create(ctx,
			larkbitable.NewCreateAppTableReqBuilder().
				AppToken(appToken).
				Body(larkbitable.NewCreateAppTableReqBodyBuilder().
					Table(larkbitable.NewReqTableBuilder().Name(name).Build()).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create table: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create table: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"table_id": resp.Data.TableId}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable create_table: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) listRecords(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable list_records: app_token and table_id are required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkbitable.NewSearchAppTableRecordReqBodyBuilder()

		if vid := stringArg(args, "view_id"); vid != "" {
			bodyBuilder.ViewId(vid)
		}
		if fns := toStringSlice(args, "field_names"); len(fns) > 0 {
			bodyBuilder.FieldNames(fns)
		}
		if filter := mapArg(args, "filter"); filter != nil {
			bodyBuilder.Filter(buildBitableFilter(filter))
		}
		if sortRaw := sliceArg(args, "sort"); len(sortRaw) > 0 {
			bodyBuilder.Sort(buildBitableSort(sortRaw))
		}
		if af, ok := boolArg(args, "automatic_fields"); ok {
			bodyBuilder.AutomaticFields(af)
		}

		reqBuilder := larkbitable.NewSearchAppTableRecordReqBuilder().
			AppToken(appToken).
			TableId(tableID).
			UserIdType("open_id").
			Body(bodyBuilder.Build())

		if ps := intArg(args, "page_size"); ps > 0 {
			reqBuilder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			reqBuilder.PageToken(pt)
		}

		resp, err := t.client.Lark().Bitable.AppTableRecord.Search(ctx, reqBuilder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list records: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list records: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		total := 0
		if resp.Data.Total != nil {
			total = *resp.Data.Total
		}
		result = map[string]any{
			"records":    resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
			"total":      total,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable list_records: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) createRecord(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	fields := mapArg(args, "fields")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable create_record: app_token and table_id are required")
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("feishu_bitable create_record: fields is required and cannot be empty")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.Create(ctx,
			larkbitable.NewCreateAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				UserIdType("open_id").
				AppTableRecord(larkbitable.NewAppTableRecordBuilder().Fields(fields).Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create record: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create record: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"record": resp.Data.Record}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable create_record: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) updateRecord(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	recordID := stringArg(args, "record_id")
	fields := mapArg(args, "fields")
	if appToken == "" || tableID == "" || recordID == "" {
		return "", fmt.Errorf("feishu_bitable update_record: app_token, table_id, and record_id are required")
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("feishu_bitable update_record: fields is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.Update(ctx,
			larkbitable.NewUpdateAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				RecordId(recordID).
				UserIdType("open_id").
				AppTableRecord(larkbitable.NewAppTableRecordBuilder().Fields(fields).Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("update record: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("update record: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"record": resp.Data.Record}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable update_record: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) deleteRecord(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	recordID := stringArg(args, "record_id")
	if appToken == "" || tableID == "" || recordID == "" {
		return "", fmt.Errorf("feishu_bitable delete_record: app_token, table_id, and record_id are required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.Delete(ctx,
			larkbitable.NewDeleteAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				RecordId(recordID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("delete record: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("delete record: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable delete_record: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true})
}

func (t *BitableTool) batchCreateRecords(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable batch_create_records: app_token and table_id are required")
	}
	records := sliceArg(args, "records")
	if len(records) == 0 {
		return "", fmt.Errorf("feishu_bitable batch_create_records: records is required")
	}
	if len(records) > maxBatchSize {
		return "", fmt.Errorf("feishu_bitable batch_create_records: max %d records per batch, got %d", maxBatchSize, len(records))
	}

	tableRecords := buildBitableRecords(records)

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.BatchCreate(ctx,
			larkbitable.NewBatchCreateAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				UserIdType("open_id").
				Body(larkbitable.NewBatchCreateAppTableRecordReqBodyBuilder().
					Records(tableRecords).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("batch create records: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("batch create records: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"records": resp.Data.Records}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable batch_create_records: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) batchUpdateRecords(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable batch_update_records: app_token and table_id are required")
	}
	records := sliceArg(args, "records")
	if len(records) == 0 {
		return "", fmt.Errorf("feishu_bitable batch_update_records: records is required")
	}
	if len(records) > maxBatchSize {
		return "", fmt.Errorf("feishu_bitable batch_update_records: max %d records per batch, got %d", maxBatchSize, len(records))
	}

	tableRecords := buildBitableRecordsWithID(records)

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.BatchUpdate(ctx,
			larkbitable.NewBatchUpdateAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				UserIdType("open_id").
				Body(larkbitable.NewBatchUpdateAppTableRecordReqBodyBuilder().
					Records(tableRecords).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("batch update records: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("batch update records: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"records": resp.Data.Records}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable batch_update_records: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) batchDeleteRecords(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable batch_delete_records: app_token and table_id are required")
	}
	recordIDs := toStringSlice(args, "record_ids")
	if len(recordIDs) == 0 {
		return "", fmt.Errorf("feishu_bitable batch_delete_records: record_ids is required")
	}
	if len(recordIDs) > maxBatchSize {
		return "", fmt.Errorf("feishu_bitable batch_delete_records: max %d records per batch, got %d", maxBatchSize, len(recordIDs))
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Bitable.AppTableRecord.BatchDelete(ctx,
			larkbitable.NewBatchDeleteAppTableRecordReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				Body(larkbitable.NewBatchDeleteAppTableRecordReqBodyBuilder().
					Records(recordIDs).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("batch delete records: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("batch delete records: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable batch_delete_records: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true})
}

func (t *BitableTool) listFields(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	if appToken == "" || tableID == "" {
		return "", fmt.Errorf("feishu_bitable list_fields: app_token and table_id are required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkbitable.NewListAppTableFieldReqBuilder().
			AppToken(appToken).
			TableId(tableID)
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Bitable.AppTableField.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list fields: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list fields: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"fields":     resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable list_fields: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *BitableTool) createField(ctx context.Context, args map[string]any) (string, error) {
	appToken := stringArg(args, "app_token")
	tableID := stringArg(args, "table_id")
	name := stringArg(args, "name")
	fieldType := intArg(args, "field_type")
	if appToken == "" || tableID == "" || name == "" || fieldType == 0 {
		return "", fmt.Errorf("feishu_bitable create_field: app_token, table_id, name, and field_type are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		fieldBuilder := larkbitable.NewAppTableFieldBuilder().
			FieldName(name).
			Type(fieldType)

		// Pass through field_property if provided (type-specific config).
		if prop := mapArg(args, "field_property"); prop != nil {
			propField := &larkbitable.AppTableFieldProperty{}
			// Serialize then deserialize to populate the struct dynamically.
			propBytes, _ := json.Marshal(prop)
			_ = json.Unmarshal(propBytes, propField)
			fieldBuilder.Property(propField)
		}

		resp, err := t.client.Lark().Bitable.AppTableField.Create(ctx,
			larkbitable.NewCreateAppTableFieldReqBuilder().
				AppToken(appToken).
				TableId(tableID).
				AppTableField(fieldBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create field: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create field: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"field": resp.Data.Field}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_bitable create_field: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

// buildBitableFilter converts a filter map to a FilterInfo struct.
func buildBitableFilter(filter map[string]any) *larkbitable.FilterInfo {
	conjunction, _ := filter["conjunction"].(string)
	if conjunction == "" {
		conjunction = "and"
	}

	builder := larkbitable.NewFilterInfoBuilder().Conjunction(conjunction)

	if conds, ok := filter["conditions"].([]any); ok {
		var conditions []*larkbitable.Condition
		for _, c := range conds {
			if cm, ok := c.(map[string]any); ok {
				cond := buildBitableCondition(cm)
				if cond != nil {
					conditions = append(conditions, cond)
				}
			}
		}
		builder.Conditions(conditions)
	}

	return builder.Build()
}

func buildBitableCondition(m map[string]any) *larkbitable.Condition {
	fieldName, _ := m["field_name"].(string)
	operator, _ := m["operator"].(string)
	if fieldName == "" || operator == "" {
		return nil
	}

	builder := larkbitable.NewConditionBuilder().
		FieldName(fieldName).
		Operator(operator)

	// isEmpty/isNotEmpty need value=[] even without actual values.
	vals := toStringSlice(m, "value")
	if vals == nil && (operator == "isEmpty" || operator == "isNotEmpty") {
		vals = []string{}
	}
	if vals != nil {
		builder.Value(vals)
	}

	return builder.Build()
}

func buildBitableSort(raw []any) []*larkbitable.Sort {
	var sorts []*larkbitable.Sort
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			fn, _ := m["field_name"].(string)
			desc, _ := m["desc"].(bool)
			if fn != "" {
				sorts = append(sorts, larkbitable.NewSortBuilder().
					FieldName(fn).Desc(desc).Build())
			}
		}
	}
	return sorts
}

func buildBitableRecords(raw []any) []*larkbitable.AppTableRecord {
	var records []*larkbitable.AppTableRecord
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			fields, _ := m["fields"].(map[string]any)
			if len(fields) > 0 {
				records = append(records, larkbitable.NewAppTableRecordBuilder().
					Fields(fields).Build())
			}
		}
	}
	return records
}

func buildBitableRecordsWithID(raw []any) []*larkbitable.AppTableRecord {
	var records []*larkbitable.AppTableRecord
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			recID, _ := m["record_id"].(string)
			fields, _ := m["fields"].(map[string]any)
			if recID != "" && len(fields) > 0 {
				records = append(records, larkbitable.NewAppTableRecordBuilder().
					RecordId(recID).Fields(fields).Build())
			}
		}
	}
	return records
}
