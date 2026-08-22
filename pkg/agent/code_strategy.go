package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

const (
	codeToolName      = "code"
	childEffectNotice = "child tool side effects may have committed; do not automatically retry"
)

var codeToolDefinition = ai.ToolDefinition{
	Name:        codeToolName,
	Description: "Run JavaScript to discover and invoke Stella tools",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{"type": "string"},
		},
		"required":             []any{"code"},
		"additionalProperties": false,
	},
}

type codeTextBlock struct {
	Type          string `json:"type"`
	Text          string `json:"text,omitempty"`
	TextSignature string `json:"textSignature,omitempty"`
	MediaID       string `json:"mediaID,omitempty"`
	Baseline      string `json:"baseline,omitempty"`
}

// codeToolValue is the tagged, JSON-only bridge protocol. Only text and
// canonical image references cross it; raw provider bytes and every other
// ContentBlock are rejected before the VM can observe them.
type codeToolValue struct {
	Blocks     []codeTextBlock        `json:"blocks"`
	References []renderrefs.Reference `json:"references,omitempty"`
	IsError    bool                   `json:"isError"`
}

// codeExecutionDetails records the only retry-relevant property of a failed
// outer code call without exposing implementation errors or bridge sentinels.
type codeExecutionDetails struct {
	ChildSideEffectsMayHaveCommitted bool `json:"childSideEffectsMayHaveCommitted"`
}

func newCodeCatalog(definitions []ai.ToolDefinition) []codemode.CatalogEntry {
	catalog := make([]codemode.CatalogEntry, 0, len(definitions))
	for _, definition := range definitions {
		catalog = append(catalog, codemode.CatalogEntry{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: cloneSchema(definition.InputSchema),
		})
	}
	return catalog
}

type codeHost struct {
	outerID      string
	tools        ToolSet
	hooks        *hooks.HookSet
	meta         hooks.HookMeta
	lifecycle    *ToolLifecycle
	canonicalize ToolImageCanonicalizer
	references   []renderrefs.Reference
	childCalls   int
}

func (h *codeHost) Invoke(ctx context.Context, invocation codemode.Invocation) (json.RawMessage, error) {
	// A child reached the shared execution core. Its external side effect may
	// have committed even if the enclosing JavaScript execution later fails.
	h.childCalls++
	var arguments map[string]any
	if len(invocation.Arguments) != 0 && string(invocation.Arguments) != "null" {
		if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("decode tools.invoke arguments: %w", err)
		}
	}
	childCall := ai.ToolCall{
		ID:        fmt.Sprintf("%s:%d", h.outerID, invocation.ID),
		Name:      invocation.Name,
		Arguments: arguments,
	}
	results, err := executeToolCalls(ctx, []ai.ToolCall{childCall}, h.tools, toolCallbacks{}, h.hooks, h.meta, h.lifecycle, h.canonicalize)
	if err != nil {
		return nil, err
	}
	result := results[0]
	h.references = append(h.references, result.References...)
	value, err := codeValueFromToolResult(result)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("serialize child result: %w", err)
	}
	if result.IsError {
		return nil, &codemode.InvocationError{Value: raw, Err: errors.New(ai.FlattenText(result.Content))}
	}
	return raw, nil
}

func codeValueFromToolResult(result ai.ToolResultMessage) (codeToolValue, error) {
	value := codeToolValue{References: result.References, IsError: result.IsError}
	for _, block := range result.Content {
		switch block := block.(type) {
		case ai.TextContent:
			// Tool text is copied through the same redactor used by tracehook
			// before it becomes script-visible. Renderref sentinels were already
			// removed by NormalizeToolResult in the shared execution core.
			value.Blocks = append(value.Blocks, codeTextBlock{Type: "text", Text: hooks.RedactToolText(block.Text), TextSignature: block.TextSignature})
		case ai.ImageRefContent:
			if err := block.Validate(); err != nil {
				return codeToolValue{}, fmt.Errorf("invalid canonical image reference: %w", err)
			}
			value.Blocks = append(value.Blocks, codeTextBlock{Type: "image_ref", MediaID: block.MediaID, Baseline: block.Baseline.Text})
		default:
			return codeToolValue{}, fmt.Errorf("code bridge rejects unsupported tool result block %T", block)
		}
	}
	return value, nil
}

func executeCodeCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))
	for _, call := range calls {
		if cb.onStart != nil {
			cb.onStart(call)
		}
		result := executeCodeCall(ctx, call, tools, definitions, hs, meta, lifecycle, canonicalize)
		result = NormalizeToolResult(result)
		results = append(results, result)
		if cb.onFinish != nil {
			cb.onFinish(result)
		}
	}
	return results, nil
}

func executeCodeCall(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ai.ToolResultMessage {
	return executeCodeCallWithLimits(ctx, call, tools, definitions, hs, meta, lifecycle, canonicalize, codemode.Limits{})
}

// executeCodeCallWithLimits keeps production on the fixed Phase 2 defaults;
// tests use it to prove that timeout exits retain already-committed metadata.
func executeCodeCallWithLimits(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer, limits codemode.Limits) ai.ToolResultMessage {
	result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name}
	if call.Name != codeToolName {
		return codeErrorResult(result, "tool not found")
	}
	if len(definitions) == 0 {
		// With no effective tools the synthetic strategy is not provider-visible.
		// Treat a forged raw code call like every other unavailable capability and
		// do not compile or execute its source.
		return codeErrorResult(result, "tool not available")
	}
	source, ok := call.Arguments["code"].(string)
	if !ok {
		return codeErrorResult(result, "code tool requires a string code argument")
	}
	host := &codeHost{
		outerID:      call.ID,
		tools:        tools,
		hooks:        hs,
		meta:         meta,
		lifecycle:    lifecycle,
		canonicalize: canonicalize,
	}
	executor, err := codemode.NewExecutor(host, limits, newCodeCatalog(definitions)...)
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	execution, err := executor.Run(ctx, source)
	if err != nil {
		return codeExecutionError(result, host, err)
	}
	result.References = dedupeReferences(host.references)
	result, err = codeResultFromJSONStrict(result, execution.JSON)
	if err != nil {
		return codeExecutionError(result, host, err)
	}
	return result
}

func codeErrorResult(result ai.ToolResultMessage, message string) ai.ToolResultMessage {
	result.IsError = true
	result.Content = []ai.ContentBlock{ai.TextContent{Text: message}}
	return result
}

func codeExecutionError(result ai.ToolResultMessage, host *codeHost, err error) ai.ToolResultMessage {
	result.References = dedupeReferences(host.references)
	if host.childCalls > 0 {
		result.Details = codeExecutionDetails{ChildSideEffectsMayHaveCommitted: true}
		return codeErrorResult(result, childEffectNotice+": "+err.Error())
	}
	return codeErrorResult(result, err.Error())
}

func codeResultFromJSON(result ai.ToolResultMessage, raw json.RawMessage) ai.ToolResultMessage {
	converted, err := codeResultFromJSONStrict(result, raw)
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	return converted
}

func codeResultFromJSONStrict(result ai.ToolResultMessage, raw json.RawMessage) (ai.ToolResultMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		blocksRaw, ok := object["blocks"]
		if !ok || !isJSONArray(blocksRaw) {
			return codeJSONResult(result, raw), nil
		}
		blocks, tagged, err := decodeTaggedCodeBlocks(blocksRaw)
		if err != nil {
			return result, err
		}
		if !tagged {
			return codeJSONResult(result, raw), nil
		}
		content := make([]ai.ContentBlock, 0, len(blocks))
		for _, block := range blocks {
			switch block.Type {
			case "text":
				content = append(content, ai.TextContent{Text: block.Text, TextSignature: block.TextSignature})
			case "image_ref":
				ref := ai.ImageRefContent{MediaID: block.MediaID, Baseline: ai.ImageBaseline{Text: block.Baseline}}
				if err := ref.Validate(); err != nil {
					return result, fmt.Errorf("invalid code image_ref: %w", err)
				}
				content = append(content, ref)
			default:
				return result, fmt.Errorf("code bridge rejects unsupported returned block type %q", block.Type)
			}
		}
		var isError bool
		if rawIsError, ok := object["isError"]; ok {
			if err := json.Unmarshal(rawIsError, &isError); err != nil {
				return result, fmt.Errorf("decode code isError: %w", err)
			}
		}
		result.Content = content
		result.IsError = isError
		// References are host-owned sideband metadata. Deliberately ignore a
		// script envelope's references field so JavaScript cannot forge it.
		return result, nil
	}
	return codeJSONResult(result, raw), nil
}

func decodeTaggedCodeBlocks(raw json.RawMessage) ([]codeTextBlock, bool, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false, fmt.Errorf("decode code blocks: %w", err)
	}
	blocks := make([]codeTextBlock, 0, len(entries))
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			return nil, false, fmt.Errorf("decode code block fields: %w", err)
		}
		rawType, ok := fields["type"]
		if !ok {
			return nil, false, nil
		}
		var blockType string
		if err := json.Unmarshal(rawType, &blockType); err != nil {
			return nil, true, fmt.Errorf("invalid code block type: %w", err)
		}
		if err := validateCodeBlockFields(blockType, fields); err != nil {
			return nil, true, err
		}
		var block codeTextBlock
		if err := json.Unmarshal(entry, &block); err != nil {
			return nil, true, fmt.Errorf("decode code block: %w", err)
		}
		blocks = append(blocks, block)
	}
	return blocks, true, nil
}

func validateCodeBlockFields(blockType string, fields map[string]json.RawMessage) error {
	allowed := map[string]struct{}{"type": {}}
	switch blockType {
	case "text":
		allowed["text"] = struct{}{}
		allowed["textSignature"] = struct{}{}
	case "image_ref":
		allowed["mediaID"] = struct{}{}
		allowed["baseline"] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("code bridge rejects unexpected field %q on %s block", field, blockType)
		}
	}
	return nil
}

func isJSONArray(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

func codeJSONResult(result ai.ToolResultMessage, raw json.RawMessage) ai.ToolResultMessage {
	result.Content = []ai.ContentBlock{ai.TextContent{Text: string(raw)}}
	return result
}

func effectiveToolSnapshot(definitions []ai.ToolDefinition, handlers ToolSet) (ToolSet, []ai.ToolDefinition) {
	tools := make(ToolSet, len(definitions))
	defs := make([]ai.ToolDefinition, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			continue
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			continue
		}
		handler, ok := handlers[definition.Name]
		if !ok {
			continue
		}
		seen[definition.Name] = struct{}{}
		tools[definition.Name] = handler
		defs = append(defs, cloneToolDefinition(definition))
	}
	return tools, defs
}

func cloneToolDefinition(definition ai.ToolDefinition) ai.ToolDefinition {
	definition.InputSchema = cloneSchema(definition.InputSchema)
	return definition
}

func cloneSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var clone map[string]any
	if json.Unmarshal(raw, &clone) != nil {
		return nil
	}
	return clone
}
