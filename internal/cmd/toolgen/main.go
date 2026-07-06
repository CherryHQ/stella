package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	inputPath  = "internal/server/docs_spec.yaml"
	outputRoot = "internal"
)

var identityFields = map[string]bool{
	"agent_id": true,
	"agentId":  true,
	"user_id":  true,
	"userId":   true,
}

// x-agent-tool accepts either a single object:
//
//	x-agent-tool: { tool: "goal", action: "create" }
//
// or a list for multiple tool actions backed by one HTTP operation:
//
//	x-agent-tool:
//	  - { tool: "scheduler", action: "update" }
//	  - { tool: "scheduler", action: "pause", fixed: { enabled: false } }
//	  - { tool: "scheduler", action: "resume", fixed: { enabled: true } }
//
// fixed fields are service-owned constants and are omitted from the model input.
// restrict narrows a generated tool schema property without changing the HTTP API,
// for example: restrict: { scope: [user, user_agent] }.
// require marks optional HTTP fields as required in the tool schema only.
// optional marks required HTTP fields as optional in the tool schema only.
func main() {
	if err := run(inputPath, outputRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input, outRoot string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", input, err)
	}
	doc, err := parseDoc(data)
	if err != nil {
		return err
	}
	tools, err := collectTools(doc)
	if err != nil {
		return err
	}
	for _, name := range sortedToolNames(tools) {
		meta, ok := domainPackages[name]
		if !ok {
			return fmt.Errorf("tool %q has no package mapping", name)
		}
		content, err := renderTool(name, meta.Package, tools[name])
		if err != nil {
			return err
		}
		outDir := filepath.Join(outRoot, meta.Dir)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(outDir, "tool_gen.go")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

type openAPIDoc struct {
	Paths      map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

type actionSpec struct {
	Tool     string           `yaml:"tool"`
	Action   string           `yaml:"action"`
	Fixed    map[string]any   `yaml:"fixed"`
	Restrict map[string][]any `yaml:"restrict"`
	Require  []string         `yaml:"require"`
	Optional []string         `yaml:"optional"`
	Batch    string           `yaml:"batch"`
}

type domainPackage struct {
	Dir     string
	Package string
}

var domainPackages = map[string]domainPackage{
	"goal":      {Dir: "goal", Package: "goal"},
	"scheduler": {Dir: "scheduler", Package: "scheduler"},
	"workflow":  {Dir: "workflow", Package: "workflow"},
	"vault":     {Dir: "vault", Package: "vault"},
	"oauth":     {Dir: "connections", Package: "connections"},
	"share":     {Dir: "share", Package: "share"},
	"recally":   {Dir: "recally", Package: "recally"},
	"email":     {Dir: "email", Package: "email"},
}

var generatedNameFallbacks = map[string]map[string]string{
	"goal":     {"CreateInput": "ToolCreateInput"},
	"workflow": {"SaveInput": "ToolSaveInput"},
}

type toolAction struct {
	Action     string
	Schema     map[string]any
	Required   []string
	Batch      string
	ItemSchema map[string]any
}

func parseDoc(data []byte) (*openAPIDoc, error) {
	var doc openAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI: %w", err)
	}
	if len(doc.Paths) == 0 || doc.Components.Schemas == nil {
		return nil, fmt.Errorf("OpenAPI paths/components missing")
	}
	return &doc, nil
}

func collectTools(doc *openAPIDoc) (map[string][]toolAction, error) {
	out := map[string][]toolAction{}
	resolver := schemaResolver{schemas: doc.Components.Schemas, stack: map[string]bool{}}
	for path, rawPath := range doc.Paths {
		pathItem, ok := rawPath.(map[string]any)
		if !ok {
			continue
		}
		pathParams := toSlice(pathItem["parameters"])
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			rawOp, ok := pathItem[method]
			if !ok {
				continue
			}
			op, ok := rawOp.(map[string]any)
			if !ok {
				continue
			}
			specs, err := parseActionSpecs(op["x-agent-tool"])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			if len(specs) == 0 {
				continue
			}
			params := append([]any{}, pathParams...)
			params = append(params, toSlice(op["parameters"])...)
			base, err := operationInputSchema(resolver, params, op)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			paramsOnly, err := paramsInputSchema(resolver, params)
			if err != nil {
				return nil, fmt.Errorf("%s %s params: %w", strings.ToUpper(method), path, err)
			}
			bodySchema, err := requestBodySchema(resolver, op)
			if err != nil {
				return nil, fmt.Errorf("%s %s body: %w", strings.ToUpper(method), path, err)
			}
			for _, spec := range specs {
				schema := cloneMap(base)
				var itemSchema map[string]any
				if spec.Batch != "" {
					itemSchema = cloneMap(bodySchema)
					for field := range identityFields {
						deleteProperty(itemSchema, field)
					}
					schema = batchInputSchema(spec.Batch, itemSchema)
				} else if len(spec.Fixed) > 0 && len(spec.Restrict) == 0 {
					schema = cloneMap(paramsOnly)
				}
				for fixed := range spec.Fixed {
					deleteProperty(schema, fixed)
					if itemSchema != nil {
						deleteProperty(itemSchema, fixed)
					}
				}
				applyRestrictions(schema, spec.Restrict)
				applyRequired(schema, spec.Require)
				applyOptional(schema, spec.Optional)
				req := stringSlice(schema["required"])
				out[spec.Tool] = append(out[spec.Tool], toolAction{Action: spec.Action, Schema: schema, Required: req, Batch: spec.Batch, ItemSchema: itemSchema})
			}
		}
	}
	return out, nil
}

func parseActionSpecs(raw any) ([]actionSpec, error) {
	if raw == nil {
		return nil, nil
	}
	var specs []actionSpec
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		if err := json.Unmarshal(b, &specs); err != nil {
			return nil, err
		}
	} else {
		var spec actionSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, err
		}
		specs = []actionSpec{spec}
	}
	for _, spec := range specs {
		if spec.Tool == "" || spec.Action == "" {
			return nil, fmt.Errorf("x-agent-tool requires tool and action")
		}
	}
	return specs, nil
}

func operationInputSchema(resolver schemaResolver, params []any, op map[string]any) (map[string]any, error) {
	schema, err := paramsInputSchema(resolver, params)
	if err != nil {
		return nil, err
	}
	bodySchema, err := requestBodySchema(resolver, op)
	if err != nil {
		return nil, err
	}
	if bodySchema != nil {
		mergeObjectSchema(schema, bodySchema)
	}
	return schema, nil
}

func paramsInputSchema(resolver schemaResolver, params []any) (map[string]any, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, raw := range params {
		param, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := param["name"].(string)
		if name == "" || identityFields[name] {
			continue
		}
		field := toolFieldName(name)
		ps, _ := param["schema"].(map[string]any)
		resolved, err := resolver.resolve(ps)
		if err != nil {
			return nil, err
		}
		props := schema["properties"].(map[string]any)
		props[field] = resolved
		if required, _ := param["required"].(bool); required {
			addRequired(schema, field)
		}
	}
	return schema, nil
}

func requestBodySchema(resolver schemaResolver, op map[string]any) (map[string]any, error) {
	rb, ok := op["requestBody"].(map[string]any)
	if !ok {
		return nil, nil
	}
	content, ok := rb["content"].(map[string]any)
	if !ok {
		return nil, nil
	}
	jsonContent, ok := content["application/json"].(map[string]any)
	if !ok {
		return nil, nil
	}
	rawSchema, ok := jsonContent["schema"].(map[string]any)
	if !ok {
		return nil, nil
	}
	resolved, err := resolver.resolve(rawSchema)
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func batchInputSchema(field string, item map[string]any) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{field},
		"properties": map[string]any{
			field: map[string]any{"type": "array", "items": item, "minItems": 1, "maxItems": 20},
		},
	}
}

func mergeObjectSchema(dst, src map[string]any) {
	props, _ := src["properties"].(map[string]any)
	for name, prop := range props {
		if identityFields[name] {
			continue
		}
		dst["properties"].(map[string]any)[name] = prop
	}
	for _, req := range stringSlice(src["required"]) {
		if !identityFields[req] {
			addRequired(dst, req)
		}
	}
}

type schemaResolver struct {
	schemas map[string]any
	stack   map[string]bool
}

func (r schemaResolver) resolve(v any) (map[string]any, error) {
	resolved, err := r.resolveAny(v)
	if err != nil {
		return nil, err
	}
	m, ok := resolved.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return m, nil
}

func (r schemaResolver) resolveAny(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		return r.resolveMap(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			resolved, err := r.resolveAny(item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return x, nil
	}
}

func (r schemaResolver) resolveMap(m map[string]any) (map[string]any, error) {
	if ref, ok := m["$ref"].(string); ok {
		base, err := r.resolveRef(ref)
		if err != nil {
			return nil, err
		}
		for _, key := range sortedKeys(m) {
			if key == "$ref" {
				continue
			}
			resolved, err := r.resolveAny(m[key])
			if err != nil {
				return nil, err
			}
			base[key] = resolved
		}
		return base, nil
	}
	merged := map[string]any{}
	if allOf, ok := m["allOf"].([]any); ok {
		for _, item := range allOf {
			resolved, err := r.resolve(item)
			if err != nil {
				return nil, err
			}
			mergeSchema(merged, resolved)
		}
	}
	for _, key := range sortedKeys(m) {
		if key == "allOf" {
			continue
		}
		resolved, err := r.resolveAny(m[key])
		if err != nil {
			return nil, err
		}
		if existing, ok := merged[key].(map[string]any); ok {
			if overlay, ok := resolved.(map[string]any); ok {
				mergeSchema(existing, overlay)
				continue
			}
		}
		merged[key] = resolved
	}
	return merged, nil
}

func (r schemaResolver) resolveRef(ref string) (map[string]any, error) {
	prefixes := []string{"#/components/schemas/", "../../components.yaml#/components/schemas/"}
	name := ""
	for _, prefix := range prefixes {
		if after, ok := strings.CutPrefix(ref, prefix); ok {
			name = after
			break
		}
	}
	if name == "" {
		return nil, fmt.Errorf("unsupported ref %q", ref)
	}
	if r.stack[name] {
		return nil, fmt.Errorf("cyclic schema ref %q", ref)
	}
	schema, ok := r.schemas[name]
	if !ok {
		return nil, fmt.Errorf("schema %q not found", name)
	}
	r.stack[name] = true
	resolved, err := r.resolve(schema)
	delete(r.stack, name)
	if err != nil {
		return nil, err
	}
	return cloneMap(resolved), nil
}

func renderTool(tool, packageName string, actions []toolAction) ([]byte, error) {
	sort.Slice(actions, func(i, j int) bool { return actions[i].Action < actions[j].Action })
	schema := toolSchema(actions)
	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by go run ./internal/cmd/toolgen; DO NOT EDIT.\n\n")
	fmt.Fprintf(&out, "package %s\n\n", packageName)
	out.WriteString("import (\n\t\"context\"\n\t\"fmt\"\n\n\t\"github.com/CherryHQ/stella/pkg/tools\"\n)\n\n")
	fmt.Fprintf(&out, "const ToolName = %q\n\n", tool)
	out.WriteString("func InputSchema() map[string]any {\n")
	out.WriteString("\treturn tools.MustInputSchema(InputSchemaJSON)\n")
	out.WriteString("}\n\n")
	out.WriteString("const InputSchemaJSON = `")
	out.Write(schemaJSON)
	out.WriteString("`\n\n")
	out.WriteString("type Handler interface {\n")
	for _, action := range actions {
		fmt.Fprintf(&out, "\t%s(context.Context, %s) (any, error)\n", exportName(action.Action), inputTypeName(tool, action.Action))
	}
	out.WriteString("}\n\n")
	for _, action := range actions {
		if action.Batch != "" {
			fmt.Fprintf(&out, "type %s struct {\n", itemTypeName(tool, action.Action))
			required := requiredSet(stringSlice(action.ItemSchema["required"]))
			for _, prop := range sortedPropertyNames(action.ItemSchema) {
				fieldName := exportName(camel(prop))
				fmt.Fprintf(&out, "\t%s %s `json:\"%s,omitempty\"`\n", fieldName, goType(action.ItemSchema["properties"].(map[string]any)[prop], required[prop]), prop)
			}
			out.WriteString("}\n\n")
		}
		fmt.Fprintf(&out, "type %s struct {\n", inputTypeName(tool, action.Action))
		if action.Batch != "" {
			fmt.Fprintf(&out, "\tItems []%s `json:\"%s,omitempty\"`\n", itemTypeName(tool, action.Action), action.Batch)
		} else {
			required := requiredSet(action.Required)
			for _, prop := range sortedPropertyNames(action.Schema) {
				fieldName := exportName(camel(prop))
				fmt.Fprintf(&out, "\t%s %s `json:\"%s,omitempty\"`\n", fieldName, goType(action.Schema["properties"].(map[string]any)[prop], required[prop]), prop)
			}
		}
		out.WriteString("}\n\n")
	}
	out.WriteString("func Dispatch(ctx context.Context, h Handler, action string, args map[string]any) (any, error) {\n")
	out.WriteString("\tswitch action {\n")
	for _, action := range actions {
		fmt.Fprintf(&out, "\tcase %q:\n", action.Action)
		fmt.Fprintf(&out, "\t\tvar in %s\n", inputTypeName(tool, action.Action))
		fmt.Fprintf(&out, "\t\tif err := tools.DecodeInput(args, &in, %#v); err != nil {\n\t\t\treturn nil, err\n\t\t}\n", action.Required)
		fmt.Fprintf(&out, "\t\treturn h.%s(ctx, in)\n", exportName(action.Action))
	}
	out.WriteString("\tdefault:\n")
	fmt.Fprintf(&out, "\t\treturn nil, fmt.Errorf(\"unknown %s action %%q\", action)\n", tool)
	out.WriteString("\t}\n}\n")
	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated %s: %w\n%s", tool, err, out.String())
	}
	return formatted, nil
}

func toolSchema(actions []toolAction) map[string]any {
	enum := make([]any, 0, len(actions))
	oneOf := make([]any, 0, len(actions))
	for _, action := range actions {
		enum = append(enum, action.Action)
		branch := cloneMap(action.Schema)
		props := branch["properties"].(map[string]any)
		props["action"] = map[string]any{"type": "string", "const": action.Action}
		addRequired(branch, "action")
		oneOf = append(oneOf, branch)
	}
	return map[string]any{
		"type":     "object",
		"required": []any{"action"},
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": enum},
		},
		"oneOf": oneOf,
	}
}

func goType(schema any, required bool) string {
	m, _ := schema.(map[string]any)
	t, _ := m["type"].(string)
	switch t {
	case "string":
		return "string"
	case "boolean":
		if required {
			return "bool"
		}
		return "*bool"
	case "integer":
		return "int"
	case "number":
		return "float64"
	case "array":
		return "[]any"
	default:
		return "map[string]any"
	}
}

func inputTypeName(tool, action string) string {
	name := exportName(action) + "Input"
	if fallback := generatedNameFallbacks[tool][name]; fallback != "" {
		return fallback
	}
	return name
}

func itemTypeName(tool, action string) string {
	return strings.TrimSuffix(inputTypeName(tool, action), "Input") + "Item"
}

func sortedToolNames(tools map[string][]toolAction) []string {
	keys := make([]string, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedPropertyNames(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	keys := make([]string, 0, len(props))
	for k := range props {
		if k != "action" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func toolFieldName(name string) string {
	switch name {
	case "jobId":
		return "id"
	case "flowId":
		return "flow_id"
	default:
		return name
	}
}

func applyRestrictions(schema map[string]any, restrict map[string][]any) {
	props, _ := schema["properties"].(map[string]any)
	for name, allowed := range restrict {
		prop, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		values := make([]any, len(allowed))
		copy(values, allowed)
		prop["enum"] = values
		if len(values) > 0 {
			prop["default"] = values[0]
		}
	}
}

func applyRequired(schema map[string]any, fields []string) {
	props, _ := schema["properties"].(map[string]any)
	for _, field := range fields {
		if _, ok := props[field]; ok {
			addRequired(schema, field)
		}
	}
}

func applyOptional(schema map[string]any, fields []string) {
	for _, field := range fields {
		removeRequired(schema, field)
	}
}

func deleteProperty(schema map[string]any, name string) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, name)
	req := stringSlice(schema["required"])
	filtered := make([]any, 0, len(req))
	for _, item := range req {
		if item != name {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		delete(schema, "required")
	} else {
		schema["required"] = filtered
	}
}

func removeRequired(schema map[string]any, name string) {
	req := stringSlice(schema["required"])
	filtered := make([]any, 0, len(req))
	for _, item := range req {
		if item != name {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		delete(schema, "required")
	} else {
		schema["required"] = filtered
	}
}

func addRequired(schema map[string]any, name string) {
	seen := map[string]bool{}
	items := stringSlice(schema["required"])
	for _, item := range items {
		seen[item] = true
	}
	if !seen[name] {
		items = append(items, name)
		sort.Strings(items)
	}
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	schema["required"] = out
}

func requiredSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func stringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toSlice(v any) []any {
	items, _ := v.([]any)
	return items
}

func mergeSchema(dst, src map[string]any) {
	for k, v := range src {
		if dk, ok := dst[k].(map[string]any); ok {
			if sk, ok := v.(map[string]any); ok {
				mergeSchema(dk, sk)
				continue
			}
		}
		dst[k] = v
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch x := v.(type) {
		case map[string]any:
			out[k] = cloneMap(x)
		case []any:
			items := make([]any, len(x))
			for i, item := range x {
				if m, ok := item.(map[string]any); ok {
					items[i] = cloneMap(m)
				} else {
					items[i] = item
				}
			}
			out[k] = items
		default:
			out[k] = x
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func exportName(s string) string {
	parts := strings.Split(s, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func camel(s string) string { return exportName(s) }
