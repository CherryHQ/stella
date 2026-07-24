package main

import (
	"strings"
	"testing"
)

func TestOperationInputSchemaMergesParamsAndBody(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/things/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: string } }
      - { name: agentId, in: path, required: true, schema: { type: string } }
    post:
      x-agent-tool: { tool: thing, action: update }
      requestBody:
        content:
          application/json:
            schema: { $ref: '#/components/schemas/ThingInput' }
components:
  schemas:
    ThingInput:
      type: object
      required: [name, agent_id]
      properties:
        name: { type: string }
        agent_id: { type: string }
        enabled: { type: boolean }
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	actions := tools["thing"]
	if len(actions) != 1 {
		t.Fatalf("actions=%d, want 1", len(actions))
	}
	props := actions[0].Schema["properties"].(map[string]any)
	if _, ok := props["agentId"]; ok {
		t.Fatal("agentId path param must be omitted")
	}
	if _, ok := props["agent_id"]; ok {
		t.Fatal("agent_id body field must be omitted")
	}
	for _, name := range []string{"id", "name", "enabled"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("property %q missing from %#v", name, props)
		}
	}
	if got := actions[0].Required; len(got) != 2 || got[0] != "id" || got[1] != "name" {
		t.Fatalf("required=%v, want [id name]", got)
	}
}

func TestToolSchemaIsPlainObjectWithActionEnum(t *testing.T) {
	schema := toolSchema([]toolAction{
		{Action: "get", Schema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}), Required: []string{"id"}},
		{Action: "list", Schema: objectSchema(nil, nil)},
	})
	props := schema["properties"].(map[string]any)
	action := props["action"].(map[string]any)
	if len(action["enum"].([]any)) != 2 {
		t.Fatalf("action enum=%#v, want two actions", action["enum"])
	}
	// Per-action requiredness cannot live in `required`, so it rides in the
	// action property's description for the model.
	if desc, _ := action["description"].(string); desc != "Required parameters by action: get(id)." {
		t.Errorf("action description=%q, want per-action required list", desc)
	}
	// OpenAI-compatible providers reject function schemas carrying combinators
	// or constraints at the top level; the wire schema must be a plain object.
	if schema["type"] != "object" {
		t.Fatalf("top-level type=%#v, want object", schema["type"])
	}
	for _, banned := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
		if _, ok := schema[banned]; ok {
			t.Errorf("top-level schema must not carry %q: %#v", banned, schema[banned])
		}
	}
}

func TestToolSchemaHoistsBranchPropertiesToTopLevel(t *testing.T) {
	schema := toolSchema([]toolAction{
		{Action: "create", Schema: objectSchema(map[string]any{"title": map[string]any{"type": "string"}}, []string{"title"})},
		{Action: "list", Schema: objectSchema(map[string]any{"q": map[string]any{"type": "string"}}, nil)},
	})
	props := schema["properties"].(map[string]any)
	// Every branch field is visible at the top level, but only `action` is required.
	for _, want := range []string{"action", "title", "q"} {
		if _, ok := props[want]; !ok {
			t.Errorf("top-level properties missing %q: %#v", want, props)
		}
	}
	// Per-action requiredness (create requires title) is deliberately absent
	// from the schema; Dispatch/DecodeInput enforce it at runtime.
	if req := schema["required"].([]any); len(req) != 1 || req[0] != "action" {
		t.Fatalf("top-level required=%#v, want [action]", req)
	}
}

func TestToolSchemaLoosensConflictingBranchTypes(t *testing.T) {
	// `inputs` is an object for one action and an array for another; the hoisted
	// top-level copy must not commit to either type, or it would contradict a branch.
	schema := toolSchema([]toolAction{
		{Action: "run", Schema: objectSchema(map[string]any{"inputs": map[string]any{"type": "object"}}, nil)},
		{Action: "save", Schema: objectSchema(map[string]any{"inputs": map[string]any{"type": "array"}}, nil)},
	})
	inputs := schema["properties"].(map[string]any)["inputs"].(map[string]any)
	if _, ok := inputs["type"]; ok {
		t.Fatalf("top-level inputs kept a conflicting type: %#v", inputs)
	}
	// The dropped type is replaced by a description naming each action's shape.
	if desc, _ := inputs["description"].(string); desc != "Type depends on action — run: object; save: array." {
		t.Errorf("inputs description=%q, want per-action type note", desc)
	}
	// A field all branches agree on keeps its type.
	schema2 := toolSchema([]toolAction{
		{Action: "a", Schema: objectSchema(map[string]any{"name": map[string]any{"type": "string"}}, nil)},
		{Action: "b", Schema: objectSchema(map[string]any{"name": map[string]any{"type": "string", "minLength": 1}}, nil)},
	})
	name := schema2["properties"].(map[string]any)["name"].(map[string]any)
	if name["type"] != "string" {
		t.Errorf("agreed type dropped: %#v", name)
	}
	if _, ok := name["minLength"]; ok {
		t.Errorf("per-branch constraint leaked to top level: %#v", name)
	}
}

func TestRestrictAnnotationNarrowsPropertyEnum(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/vault:
    get:
      x-agent-tool: { tool: vault, action: list, restrict: { scope: [user, user_agent] } }
      parameters:
        - name: scope
          in: query
          schema: { type: string, enum: [user, user_agent, system, system_agent], default: user }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	props := tools["vault"][0].Schema["properties"].(map[string]any)
	scope := props["scope"].(map[string]any)
	enum := scope["enum"].([]any)
	if len(enum) != 2 || enum[0] != "user" || enum[1] != "user_agent" {
		t.Fatalf("scope enum=%#v, want [user user_agent]", enum)
	}
}

func TestRequireAnnotationMarksOptionalFieldRequired(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/email/send:
    post:
      x-agent-tool: { tool: email, action: send, require: [idempotency_key] }
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [to, subject, body]
              properties:
                to: { type: array, items: { type: string } }
                subject: { type: string }
                body: { type: string }
                idempotency_key: { type: string }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	got := tools["email"][0].Required
	want := []string{"body", "idempotency_key", "subject", "to"}
	if len(got) != len(want) {
		t.Fatalf("required=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("required=%v, want %v", got, want)
		}
	}
}

func TestOptionalAnnotationMarksRequiredFieldOptional(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/feeds/{id}/poll:
    parameters:
      - { name: id, in: path, required: true, schema: { type: string } }
    post:
      x-agent-tool: { tool: recally, action: feed_poll, optional: [id] }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	got := tools["recally"][0].Required
	want := []string{}
	if len(got) != len(want) {
		t.Fatalf("required=%v, want %v", got, want)
	}
}

func TestMultiActionAnnotationOmitFixedFields(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/jobs/{jobId}/{flowId}:
    parameters:
      - { name: jobId, in: path, required: true, schema: { type: string } }
      - { name: flowId, in: path, required: true, schema: { type: string } }
      - { name: agentId, in: path, required: true, schema: { type: string } }
    patch:
      x-agent-tool:
        - { tool: scheduler, action: update }
        - { tool: scheduler, action: pause, fixed: { enabled: false } }
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                enabled: { type: boolean }
                name: { type: string }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	actions := map[string]toolAction{}
	for _, action := range tools["scheduler"] {
		actions[action.Action] = action
	}
	if _, ok := actions["update"]; !ok {
		t.Fatal("update action missing")
	}
	pauseProps := actions["pause"].Schema["properties"].(map[string]any)
	if _, ok := pauseProps["enabled"]; ok {
		t.Fatal("fixed enabled field must be omitted from pause input")
	}
	if _, ok := pauseProps["id"]; !ok {
		t.Fatal("jobId path param should become model-friendly id")
	}
	if _, ok := pauseProps["flow_id"]; !ok {
		t.Fatal("flowId path param should become model-friendly flow_id")
	}
}

func TestBatchAnnotationWrapsRequestBody(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/articles:
    post:
      x-agent-tool: { tool: recally, action: save, batch: articles }
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [url]
              properties:
                url: { type: string }
                title: { type: string }
                agent_id: { type: string }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	action := tools["recally"][0]
	if action.Batch != "articles" {
		t.Fatalf("Batch=%q, want articles", action.Batch)
	}
	props := action.Schema["properties"].(map[string]any)
	articles := props["articles"].(map[string]any)
	if articles["minItems"] != 1 || articles["maxItems"] != 20 {
		t.Fatalf("articles bounds=%#v", articles)
	}
	itemProps := articles["items"].(map[string]any)["properties"].(map[string]any)
	if _, ok := itemProps["agent_id"]; ok {
		t.Fatal("identity field must be omitted from batch item")
	}
	if _, ok := itemProps["url"]; !ok {
		t.Fatal("url missing from batch item")
	}
	if got := action.Required; len(got) != 1 || got[0] != "articles" {
		t.Fatalf("required=%v, want [articles]", got)
	}
}

func TestRenderToolUsesPackageTrimmedNamesAndCamelActions(t *testing.T) {
	out, err := renderTool("goal", "goal", []toolAction{{Action: "create", Schema: objectSchema(nil, nil)}})
	if err != nil {
		t.Fatalf("render goal: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "package goal") || !strings.Contains(text, `const ToolName = "goal"`) || !strings.Contains(text, "type ToolCreateInput struct") {
		t.Fatalf("goal render did not use package/fallback name:\n%s", text)
	}
	out, err = renderTool("recally", "recally", []toolAction{{Action: "list_articles", Schema: objectSchema(nil, nil)}})
	if err != nil {
		t.Fatalf("render recally: %v", err)
	}
	text = string(out)
	if !strings.Contains(text, "ListArticles(context.Context, ListArticlesInput)") || strings.Contains(text, "List_articles") {
		t.Fatalf("recally render did not camel-case action:\n%s", text)
	}
}

func TestFixedWithRestrictKeepsBodyFields(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/shares:
    post:
      x-agent-tool:
        - { tool: share, action: artifact, fixed: { source: artifact, article_id: "" }, restrict: { source: [artifact] } }
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [source]
              properties:
                source: { type: string, enum: [artifact, article] }
                path: { type: string }
                article_id: { type: string }
                expires_in: { type: string }
components:
  schemas: {}
`))
	tools, err := collectTools(doc)
	if err != nil {
		t.Fatalf("collectTools: %v", err)
	}
	props := tools["share"][0].Schema["properties"].(map[string]any)
	if _, ok := props["source"]; ok {
		t.Fatal("fixed source field must be omitted")
	}
	if _, ok := props["article_id"]; ok {
		t.Fatal("fixed article_id field must be omitted")
	}
	if _, ok := props["path"]; !ok {
		t.Fatal("body path field should remain")
	}
	if _, ok := props["expires_in"]; !ok {
		t.Fatal("body expires_in field should remain")
	}
}

func mustDoc(t *testing.T, data []byte) *openAPIDoc {
	t.Helper()
	doc, err := parseDoc(data)
	if err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	return doc
}

func objectSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": props}
	for _, req := range required {
		addRequired(schema, req)
	}
	return schema
}
