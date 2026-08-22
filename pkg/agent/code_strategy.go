package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

const codeToolName = "code"

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

type codeToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type codeToolDescription struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type codeTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codeToolValue is the narrow Phase 2 bridge protocol. Images and other
// content fidelity are intentionally deferred to Phase 3.
type codeToolValue struct {
	Blocks     []codeTextBlock        `json:"blocks"`
	References []renderrefs.Reference `json:"references,omitempty"`
	IsError    bool                   `json:"isError"`
}

type codeCatalog struct {
	definitions []ai.ToolDefinition
	byName      map[string]ai.ToolDefinition
}

func newCodeCatalog(definitions []ai.ToolDefinition) codeCatalog {
	catalog := codeCatalog{
		definitions: cloneToolDefinitions(definitions),
		byName:      make(map[string]ai.ToolDefinition, len(definitions)),
	}
	for _, definition := range catalog.definitions {
		catalog.byName[definition.Name] = definition
	}
	return catalog
}

func (c codeCatalog) Search(query string) (any, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	results := make([]codeToolSummary, 0, len(c.definitions))
	for _, definition := range c.definitions {
		haystack := strings.ToLower(definition.Name + " " + definition.Description)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		results = append(results, codeToolSummary{Name: definition.Name, Description: definition.Description})
	}
	return results, nil
}

func (c codeCatalog) Describe(name string) (any, error) {
	definition, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	return codeToolDescription{
		Name:        definition.Name,
		Description: definition.Description,
		InputSchema: cloneSchema(definition.InputSchema),
	}, nil
}

type codeHost struct {
	outerID      string
	tools        ToolSet
	hooks        *hooks.HookSet
	meta         hooks.HookMeta
	lifecycle    *ToolLifecycle
	canonicalize ToolImageCanonicalizer
	catalog      codeCatalog
	references   []renderrefs.Reference
}

func (h *codeHost) Search(query string) (any, error) { return h.catalog.Search(query) }

func (h *codeHost) Describe(name string) (any, error) { return h.catalog.Describe(name) }

func (h *codeHost) Invoke(ctx context.Context, invocation codemode.Invocation) (json.RawMessage, error) {
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
		text, ok := block.(ai.TextContent)
		if !ok {
			return codeToolValue{}, fmt.Errorf("code bridge supports text tool results only, got %T", block)
		}
		value.Blocks = append(value.Blocks, codeTextBlock{Type: "text", Text: text.Text})
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
	result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name}
	if call.Name != codeToolName {
		return codeErrorResult(result, "tool not found")
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
		catalog:      newCodeCatalog(definitions),
	}
	executor, err := codemode.NewExecutor(host, codemode.Limits{})
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	execution, err := executor.Run(ctx, source)
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	result.References = dedupeReferences(host.references)
	return codeResultFromJSON(result, execution.JSON)
}

func codeErrorResult(result ai.ToolResultMessage, message string) ai.ToolResultMessage {
	result.IsError = true
	result.Content = []ai.ContentBlock{ai.TextContent{Text: message}}
	return result
}

func codeResultFromJSON(result ai.ToolResultMessage, raw json.RawMessage) ai.ToolResultMessage {
	var envelope struct {
		Blocks     json.RawMessage        `json:"blocks"`
		References []renderrefs.Reference `json:"references"`
		IsError    bool                   `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Blocks != nil {
		var blocks []codeTextBlock
		if err := json.Unmarshal(envelope.Blocks, &blocks); err != nil {
			return codeErrorResult(result, "invalid ToolValue blocks: "+err.Error())
		}
		content := make([]ai.ContentBlock, 0, len(blocks))
		for _, block := range blocks {
			if block.Type != "text" {
				return codeErrorResult(result, "unsupported ToolValue block type: "+block.Type)
			}
			content = append(content, ai.TextContent{Text: block.Text})
		}
		result.Content = content
		result.IsError = envelope.IsError
		result.References = dedupeReferences(append(result.References, envelope.References...))
		return result
	}
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

func cloneToolDefinitions(definitions []ai.ToolDefinition) []ai.ToolDefinition {
	out := make([]ai.ToolDefinition, len(definitions))
	for i, definition := range definitions {
		out[i] = cloneToolDefinition(definition)
	}
	return out
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
