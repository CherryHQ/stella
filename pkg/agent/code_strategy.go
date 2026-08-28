package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

const (
	codeToolName      = "code"
	childEffectNotice = "child tool side effects may have committed; do not automatically retry"
	codeValueKind     = "stella.tool_value"
	codeValueVersion  = 1
	// issuedImageLimit reuses the bridge payload ceiling for VM-external image
	// provenance. Keep this fixed with codemode's Phase 3 payload budget.
	issuedImageLimit      = 1 << 20
	issuedImageOverhead   = 128
	issuedPreviewLimit    = 4 << 10
	issuedImageMaxCount   = 64
	codeReferenceMaxCount = 64
)

var codeToolDefinition = ai.ToolDefinition{
	Name:        codeToolName,
	Description: "Run JavaScript for one authorized tool that is not directly listed, or for a short orchestration whose intermediate results should flow between tools without returning to the model. Do not wrap a standalone directly listed tool in Code. When listed, use direct bash for standalone or potentially long-running shell/file/git/package/script/process work, and direct view_image for pixels. Inside Code, invoke bash when direct bash is unavailable or when its output feeds another tool in the same script. The catalog contains the complete authorized Stella/MCP tool set. Directly listed names, bash, skills, memory, view_image, and names documented by a loaded skill are exact and never require discovery. Search once only when the capability or tool name is unknown; a non-empty search with at most 3 matches includes inputSchema, so invoke that result without describe. Describe only when the selected search result omitted its schema. The VM has no ambient filesystem, process, network, timer, or module-import capability; tools.invoke(\"bash\", ...) uses the same sandbox and policy as direct bash. API: tools.search(query, offset?) returns up to 20 matches. An empty query lists tools, and the returned array carries non-enumerable hasMore and nextOffset properties. tools.describe(name) returns the exact description and inputSchema. tools.invoke(name, args?) resolves to a structured tool result. Use tools.text(value) to join text blocks or tools.json(value) when that text is JSON; the same helpers accept ToolInvocationError.value. Returning a structured tool result directly preserves its text, images, references, and error state. Return other values only when JSON-serializable; returning nothing yields null. Fixed limits per call: 30 seconds wall clock including child tools, 64 child calls, and 1 MiB arguments, child results, and final result. Child calls run one at a time even under Promise.all. Console output is discarded. Tool text is secret-redacted before JavaScript sees it.",
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
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Token   string `json:"token,omitempty"`
	Preview string `json:"preview,omitempty"`
}

// codeToolValue is the tagged, JSON-only bridge protocol. Only text and
// canonical image references cross it; raw provider bytes and every other
// ContentBlock are rejected before the VM can observe them.
type codeToolValue struct {
	Kind       string                 `json:"kind"`
	Version    int                    `json:"version"`
	Blocks     []codeTextBlock        `json:"blocks"`
	References []renderrefs.Reference `json:"references,omitempty"`
	IsError    bool                   `json:"isError"`
}

// codeExecutionDetails records the only retry-relevant property of a failed
// outer code call without exposing implementation errors or bridge sentinels.
type codeExecutionDetails struct {
	ChildSideEffectsMayHaveCommitted bool   `json:"childSideEffectsMayHaveCommitted"`
	Code                             string `json:"code"`
	Actual                           int    `json:"actual,omitempty"`
	Limit                            int    `json:"limit,omitempty"`
	Terminal                         bool   `json:"terminal,omitempty"`
}

func redactChildArguments(arguments map[string]any, secretValues []string) map[string]any {
	if len(arguments) == 0 {
		return map[string]any{}
	}
	raw, err := json.Marshal(redactExactValues(arguments, secretValues))
	if err != nil {
		return map[string]any{"redacted": true}
	}
	redacted := hooks.RedactToolText(string(raw))
	var out map[string]any
	if json.Unmarshal([]byte(redacted), &out) != nil {
		return map[string]any{"redacted": true}
	}
	return out
}

func redactExactValues(value any, secretValues []string) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = redactExactValues(child, secretValues)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, child := range value {
			out[index] = redactExactValues(child, secretValues)
		}
		return out
	case string:
		return hooks.RedactSecretValues(value, secretValues)
	default:
		return value
	}
}

func redactChildResult(result ai.ToolResultMessage, secretValues []string) ai.ToolResultMessage {
	redacted := result
	redacted.Content = make([]ai.ContentBlock, 0, len(result.Content))
	for _, block := range result.Content {
		switch block := block.(type) {
		case ai.TextContent:
			block.Text = hooks.RedactToolText(hooks.RedactSecretValues(block.Text, secretValues))
			block.TextSignature = ""
			redacted.Content = append(redacted.Content, block)
		case ai.ImageRefContent:
			block.Baseline.Text = hooks.RedactToolText(hooks.RedactSecretValues(block.Baseline.Text, secretValues))
			redacted.Content = append(redacted.Content, block)
		default:
			redacted.Content = append(redacted.Content, ai.TextContent{Text: "[unsupported child result omitted]"})
		}
	}
	return redacted
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

var codeHotToolNames = map[string]struct{}{
	"bash":       {},
	"memory":     {},
	"skills":     {},
	"view_image": {},
}

func codeDirectToolNames(surface CodeToolSurface) map[string]struct{} {
	switch surface {
	case CodeToolSurfaceBash:
		return map[string]struct{}{"bash": {}}
	case CodeToolSurfaceOnly:
		return nil
	default:
		return codeHotToolNames
	}
}

// codeModeToolSurface exposes the selected direct subset while letting Code
// orchestrate the complete effective tool snapshot. The production hot surface
// keeps Code hidden for a bash-only snapshot; evaluation surfaces keep their
// protocol stable even when only bash is admitted.
func codeModeToolSurface(tools ToolSet, definitions []ai.ToolDefinition, surface CodeToolSurface) (ToolSet, ToolSet, []ai.ToolDefinition, []ai.ToolDefinition) {
	if len(definitions) == 0 {
		return nil, nil, nil, nil
	}
	directNames := codeDirectToolNames(surface)
	directTools := make(ToolSet)
	codeTools := make(ToolSet)
	providerDefs := make([]ai.ToolDefinition, 0, len(directNames)+1)
	codeDefs := make([]ai.ToolDefinition, 0, len(definitions))
	hasNonBash := false
	for _, definition := range definitions {
		tool, ok := tools[definition.Name]
		if !ok {
			continue
		}
		codeTools[definition.Name] = tool
		codeDefs = append(codeDefs, cloneToolDefinition(definition))
		if definition.Name != "bash" {
			hasNonBash = true
		}
		if _, direct := directNames[definition.Name]; direct {
			directTools[definition.Name] = tool
			providerDefs = append(providerDefs, cloneToolDefinition(definition))
		}
	}
	if surface == CodeToolSurfaceHot && !hasNonBash {
		return directTools, nil, providerDefs, nil
	}
	if len(codeDefs) == 0 {
		return directTools, nil, providerDefs, nil
	}
	providerDefs = append(providerDefs, cloneToolDefinition(codeToolDefinition))
	return directTools, codeTools, providerDefs, codeDefs
}

type codeHost struct {
	outerID        string
	tools          ToolSet
	hooks          *hooks.HookSet
	meta           hooks.HookMeta
	lifecycle      *ToolLifecycle
	canonicalize   ToolImageCanonicalizer
	references     []renderrefs.Reference
	issuedImages   map[string]issuedImageRef
	issuedBytes    int
	referenceBytes int
	referenceSeen  map[string]struct{}
	childCalls     int
	childAudit     []ai.ChildToolCallAudit
	callbacks      toolCallbacks
}

// issuedImageRef never crosses the VM boundary. The token is capability-like
// only inside this execution: it maps to the exact child-issued canonical pair.
type issuedImageRef struct {
	mediaID  string
	baseline string
}

func (h *codeHost) Invoke(ctx context.Context, invocation codemode.Invocation) (json.RawMessage, error) {
	// The executor admits at most Limits.MaxCalls invocations. Record its
	// attempt before doing any host-side decoding or lifecycle work so durable
	// evaluation evidence cannot disappear behind an infrastructure failure.
	auditIndex := len(h.childAudit)
	h.childAudit = append(h.childAudit, ai.ChildToolCallAudit{
		ID:        fmt.Sprintf("%s:%d", h.outerID, invocation.ID),
		Name:      invocation.Name,
		IsError:   true,
		ErrorKind: ai.ToolErrorKindTool,
	})
	auditResolved := false
	defer func() {
		if !auditResolved {
			// A single invocation enters executeToolCalls below. Any returned
			// infrastructure error therefore means this attempt has no usable
			// result, even when a child may already have reached the sandbox.
			h.childAudit[auditIndex].IsError = true
			h.childAudit[auditIndex].ErrorKind = ai.ToolErrorKindTool
		}
	}()
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
	childStarted := false
	childCallbacks := toolCallbacks{
		onExecute: func(call ai.ToolCall) {
			// Only reaching the handler makes a side effect possible. Missing tools
			// and lifecycle/hook blocks remain auditable attempts without the
			// misleading do-not-retry warning.
			h.childCalls++
			childStarted = true
			if h.callbacks.onChildStart != nil {
				h.callbacks.onChildStart(h.outerID, call)
			}
		},
		onFinish: func(result ai.ToolResultMessage) {
			if !childStarted && h.callbacks.onChildStart != nil {
				// Missing and policy-blocked attempts never reach a handler, but UI
				// tool streams still require a start before the settled result.
				h.callbacks.onChildStart(h.outerID, childCall)
			}
			if h.callbacks.onChildFinish != nil {
				h.callbacks.onChildFinish(h.outerID, result)
			}
		},
	}
	results, err := executeToolCalls(ctx, []ai.ToolCall{childCall}, h.tools, childCallbacks, h.hooks, h.meta, h.lifecycle, h.canonicalize)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, errors.New("code child execution produced no result")
	}
	result := results[0]
	// The shared execution core owns lifecycle, hook, canonicalization, and the
	// final structured error classification. Record exactly that verdict here,
	// before bridge serialization can fail, without copying arguments or tool
	// content into durable Code Mode metadata.
	h.childAudit[auditIndex].ID = result.ToolCallID
	h.childAudit[auditIndex].Name = result.ToolName
	h.childAudit[auditIndex].IsError = result.IsError
	h.childAudit[auditIndex].ErrorKind = result.ErrorKind
	auditResolved = true
	if err := h.preflightToolResult(result); err != nil {
		return nil, err
	}
	if err := h.addReferences(result.References); err != nil {
		return nil, err
	}
	value, err := h.codeValueFromToolResult(result)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("serialize child result: %w", err)
	}
	if result.IsError {
		// Business failure is intentionally safe and stable. The normalized value
		// carries script-visible detail without promoting raw tool error strings.
		return nil, &codemode.InvocationError{Value: raw, Err: errors.New("tool invocation failed")}
	}
	return raw, nil
}

func (h *codeHost) preflightToolResult(result ai.ToolResultMessage) error {
	budget := 0
	for _, block := range result.Content {
		var n int
		switch block := block.(type) {
		case ai.TextContent:
			n = len(block.Text)
		case ai.ImageRefContent:
			if err := block.Validate(); err != nil {
				return fmt.Errorf("invalid canonical image reference: %w", err)
			}
			n = len(block.MediaID) + len(block.Baseline.Text)
		default:
			return fmt.Errorf("code bridge rejects unsupported tool result block %T", block)
		}
		if n > issuedImageLimit-budget {
			return codemode.ErrPayloadTooLarge
		}
		budget += n
	}
	return nil
}

func (h *codeHost) addReferences(refs []renderrefs.Reference) error {
	for _, ref := range refs {
		key := ref.Type + "\x00" + ref.ID
		if _, seen := h.referenceSeen[key]; seen {
			continue
		}
		if len(h.references) >= codeReferenceMaxCount {
			return codemode.ErrPayloadTooLarge
		}
		raw, err := json.Marshal(ref)
		if err != nil || len(raw) > issuedImageLimit-h.referenceBytes {
			return codemode.ErrPayloadTooLarge
		}
		if h.referenceSeen == nil {
			h.referenceSeen = make(map[string]struct{})
		}
		h.referenceSeen[key] = struct{}{}
		h.referenceBytes += len(raw)
		h.references = append(h.references, ref)
	}
	return nil
}

func (h *codeHost) codeValueFromToolResult(result ai.ToolResultMessage) (codeToolValue, error) {
	value := codeToolValue{Kind: codeValueKind, Version: codeValueVersion, References: result.References, IsError: result.IsError}
	for _, block := range result.Content {
		switch block := block.(type) {
		case ai.TextContent:
			// Tool text is copied through the same redactor used by tracehook
			// before it becomes script-visible. Renderref sentinels were already
			// removed by NormalizeToolResult in the shared execution core.
			// This is deliberately stricter than the native path, where tool
			// text reaches the model verbatim: a script can forward text into
			// another call without a human ever seeing it. The `code` tool
			// description states the rewrite, so it is never silent.
			value.Blocks = append(value.Blocks, codeTextBlock{Type: "text", Text: hooks.RedactToolText(block.Text)})
		case ai.ImageRefContent:
			if err := block.Validate(); err != nil {
				return codeToolValue{}, fmt.Errorf("invalid canonical image reference: %w", err)
			}
			preview := boundedImagePreview(hooks.RedactToolText(block.Baseline.Text))
			token, err := h.issueImageRef(block, preview)
			if err != nil {
				return codeToolValue{}, err
			}
			// The VM gets only a redacted preview. The exact baseline remains
			// host-owned and is restored only by the token.
			value.Blocks = append(value.Blocks, codeTextBlock{Type: "image_ref", Token: token, Preview: preview})
		default:
			return codeToolValue{}, fmt.Errorf("code bridge rejects unsupported tool result block %T", block)
		}
	}
	return value, nil
}

func boundedImagePreview(preview string) string {
	if len(preview) <= issuedPreviewLimit {
		return preview
	}
	return preview[:issuedPreviewLimit]
}

func (h *codeHost) issueImageRef(block ai.ImageRefContent, preview string) (string, error) {
	if len(h.issuedImages) >= issuedImageMaxCount {
		return "", fmt.Errorf("issued image reference count exceeds bridge payload limit: %w", codemode.ErrPayloadTooLarge)
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("issue image reference token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	entryBytes := len(token) + len(block.MediaID) + len(block.Baseline.Text) + len(preview) + issuedImageOverhead
	if entryBytes > issuedImageLimit-h.issuedBytes {
		return "", fmt.Errorf("issued image references exceed bridge payload limit: %w", codemode.ErrPayloadTooLarge)
	}
	if h.issuedImages == nil {
		h.issuedImages = make(map[string]issuedImageRef)
	}
	h.issuedImages[token] = issuedImageRef{mediaID: block.MediaID, baseline: block.Baseline.Text}
	h.issuedBytes += entryBytes
	return token, nil
}

func (h *codeHost) releaseIssuedImages() {
	clear(h.issuedImages)
	h.issuedImages = nil
	h.issuedBytes = 0
}

func executeCodeModeCalls(ctx context.Context, calls []ai.ToolCall, directTools, codeTools ToolSet, codeDefinitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))
	for _, call := range calls {
		if call.Name == codeToolName {
			if cb.onStart != nil {
				cb.onStart(call)
			}
			var result ai.ToolResultMessage
			if codeTools == nil {
				result = codeErrorResult(ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name}, "tool not available")
			} else {
				result = executeCodeCallWithCallbacks(ctx, call, codeTools, codeDefinitions, cb, hs, meta, lifecycle, canonicalize)
			}
			results = append(results, result)
			if cb.onFinish != nil {
				cb.onFinish(result)
			}
			// The turn ends here, so the calls the provider requested alongside
			// this one must not start. Waiting for the batch to drain would run
			// side effects after the caller has already gone away.
			if details, ok := result.Details.(codeExecutionDetails); ok && details.Terminal {
				return results, nil
			}
			continue
		}
		directResults, err := executeToolCalls(ctx, []ai.ToolCall{call}, directTools, cb, hs, meta, lifecycle, canonicalize)
		results = append(results, directResults...)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

// executeCodeCalls is retained as the focused code-tool executor used by unit
// tests. Production Code Mode routes direct bash calls through
// executeCodeModeCalls instead.
func executeCodeCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	return executeCodeModeCalls(ctx, calls, nil, tools, definitions, cb, hs, meta, lifecycle, canonicalize)
}

func executeCodeCall(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ai.ToolResultMessage {
	return executeCodeCallWithCallbacks(ctx, call, tools, definitions, toolCallbacks{}, hs, meta, lifecycle, canonicalize)
}

func executeCodeCallWithCallbacks(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ai.ToolResultMessage {
	return executeCodeCallWithLimitsAndCallbacks(ctx, call, tools, definitions, cb, hs, meta, lifecycle, canonicalize, codemode.Limits{})
}

// executeCodeCallWithLimits keeps production on the fixed Phase 2 defaults;
// tests use it to prove that timeout exits retain already-committed metadata.
func executeCodeCallWithLimits(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer, limits codemode.Limits) ai.ToolResultMessage {
	return executeCodeCallWithLimitsAndCallbacks(ctx, call, tools, definitions, toolCallbacks{}, hs, meta, lifecycle, canonicalize, limits)
}

func executeCodeCallWithLimitsAndCallbacks(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer, limits codemode.Limits) ai.ToolResultMessage {
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
		callbacks:    cb,
	}
	defer host.releaseIssuedImages()
	executor, err := codemode.NewExecutor(host, limits, newCodeCatalog(definitions)...)
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	execution, err := executor.Run(ctx, source)
	if err != nil {
		result.ChildToolCalls = append([]ai.ChildToolCallAudit(nil), host.childAudit...)
		return codeExecutionError(result, host, err)
	}
	result.References = dedupeReferences(host.references)
	result, err = codeResultFromJSONStrictWithIssuedImages(result, execution.JSON, host.issuedImages)
	if err != nil {
		result.ChildToolCalls = append([]ai.ChildToolCallAudit(nil), host.childAudit...)
		return codeExecutionError(result, host, err)
	}
	result.ChildToolCalls = append([]ai.ChildToolCallAudit(nil), host.childAudit...)
	return result
}

func codeErrorResult(result ai.ToolResultMessage, message string) ai.ToolResultMessage {
	result.IsError = true
	result.Content = []ai.ContentBlock{ai.TextContent{Text: message}}
	return result
}

func codeExecutionError(result ai.ToolResultMessage, host *codeHost, err error) ai.ToolResultMessage {
	if len(result.ChildToolCalls) == 0 && len(host.childAudit) > 0 {
		result.ChildToolCalls = append([]ai.ChildToolCallAudit(nil), host.childAudit...)
	}
	result.References = dedupeReferences(host.references)
	code := "code_infrastructure_failure"
	message := "code tool infrastructure failure"
	terminal := false
	switch {
	case errors.Is(err, codemode.ErrTimedOut):
		// The 30s budget covers the child tools the script invoked, so one slow
		// tool must not silently end the turn: report it and let the model decide
		// what to do next, the way a native tool timeout does.
		code, message = "code_execution_timed_out", "code execution timed out; the fixed budget includes the time spent in the tools the script invoked"
	case errors.Is(err, codemode.ErrCancelled):
		code, message, terminal = "code_execution_cancelled", "code execution cancelled", true
	case errors.Is(err, codemode.ErrSourceTooLarge):
		code, message = "code_source_too_large", "code source exceeded the fixed byte limit"
	case errors.Is(err, codemode.ErrPayloadTooLarge):
		code, message = "code_payload_too_large", "code invocation or child result exceeded the fixed payload byte limit"
	case errors.Is(err, codemode.ErrResultTooLarge):
		code, message = "code_result_too_large", "code result exceeded the fixed byte limit"
	case errors.Is(err, codemode.ErrInvocationLimit):
		code, message = "code_invocation_limit", "code execution exceeded the fixed child-call limit"
	case strings.HasPrefix(err.Error(), "javascript execution failed:"):
		// This is guest source failure, not infrastructure. It remains the normal
		// JavaScript error surface, while child business rejection itself is safe.
		code, message = "code_javascript_error", err.Error()
	default:
		// Original infrastructure detail stays in the process log only, after the
		// shared redactor. It must never become model-visible tool content.
		slog.Error("code tool infrastructure failure", "error", hooks.RedactToolText(err.Error()))
	}
	details := codeExecutionDetails{ChildSideEffectsMayHaveCommitted: host.childCalls > 0, Code: code, Terminal: terminal}
	var limit *codemode.LimitError
	if errors.As(err, &limit) {
		details.Actual = limit.Actual
		details.Limit = limit.Limit
		if limit.Code != "" {
			details.Code = limit.Code
		}
		if limit.Actual > 0 && limit.Limit > 0 {
			message = fmt.Sprintf("%s (actual %d, limit %d)", message, limit.Actual, limit.Limit)
		}
	}
	result.Details = details
	if host.childCalls > 0 {
		return codeErrorResult(result, childEffectNotice+": "+message)
	}
	return codeErrorResult(result, message)
}

func codeResultFromJSON(result ai.ToolResultMessage, raw json.RawMessage) ai.ToolResultMessage {
	converted, err := codeResultFromJSONStrict(result, raw)
	if err != nil {
		return codeErrorResult(result, err.Error())
	}
	return converted
}

func codeResultFromJSONStrict(result ai.ToolResultMessage, raw json.RawMessage) (ai.ToolResultMessage, error) {
	return codeResultFromJSONStrictWithIssuedImages(result, raw, nil)
}

func codeResultFromJSONStrictWithIssuedImages(result ai.ToolResultMessage, raw json.RawMessage, issued map[string]issuedImageRef) (ai.ToolResultMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		if !isCodeToolValue(object) {
			return codeJSONResult(result, raw), nil
		}
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
		content := make([]ai.ContentBlock, 0, min(len(blocks), issuedImageMaxCount))
		seenImages := make(map[string]struct{}, len(blocks))
		imageBytes := 0
		for _, block := range blocks {
			switch block.Type {
			case "text":
				// Script output has no render-reference authority. Escaping prevents a
				// later generic normalization pass from promoting a forged sentinel.
				content = append(content, ai.TextContent{Text: escapeCodeRenderRefs(block.Text)})
			case "image_ref":
				ref, ok := issued[block.Token]
				if !ok {
					return result, errors.New("code bridge rejects unissued image reference")
				}
				if _, seen := seenImages[block.Token]; seen {
					continue
				}
				if len(seenImages) >= issuedImageMaxCount || len(ref.mediaID)+len(ref.baseline) > issuedImageLimit-imageBytes {
					return result, codemode.ErrPayloadTooLarge
				}
				seenImages[block.Token] = struct{}{}
				imageBytes += len(ref.mediaID) + len(ref.baseline)
				content = append(content, ai.ImageRefContent{MediaID: ref.mediaID, Baseline: ai.ImageBaseline{Text: ref.baseline}})
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

func isCodeToolValue(object map[string]json.RawMessage) bool {
	var kind string
	var version int
	return json.Unmarshal(object["kind"], &kind) == nil &&
		json.Unmarshal(object["version"], &version) == nil &&
		kind == codeValueKind && version == codeValueVersion
}

func escapeCodeRenderRefs(text string) string {
	return strings.ReplaceAll(text, "::stella-ref/v1::", `\\::stella-ref/v1::`)
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
	case "image_ref":
		allowed["token"] = struct{}{}
		allowed["preview"] = struct{}{}
	default:
		return fmt.Errorf("code bridge rejects unsupported returned block type %q", blockType)
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
