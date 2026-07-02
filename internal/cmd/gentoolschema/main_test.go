package main

import "testing"

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

func TestToolSchemaUsesActionEnumAndOneOf(t *testing.T) {
	schema := toolSchema([]toolAction{
		{Action: "get", Schema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"})},
		{Action: "list", Schema: objectSchema(nil, nil)},
	})
	props := schema["properties"].(map[string]any)
	action := props["action"].(map[string]any)
	if len(action["enum"].([]any)) != 2 {
		t.Fatalf("action enum=%#v, want two actions", action["enum"])
	}
	oneOf := schema["oneOf"].([]any)
	if len(oneOf) != 2 {
		t.Fatalf("oneOf branches=%d, want 2", len(oneOf))
	}
	branch := oneOf[0].(map[string]any)
	branchProps := branch["properties"].(map[string]any)
	if branchProps["action"].(map[string]any)["const"] == "" {
		t.Fatal("oneOf branch missing action const")
	}
}

func TestMultiActionAnnotationOmitFixedFields(t *testing.T) {
	doc := mustDoc(t, []byte(`
paths:
  /api/jobs/{jobId}:
    parameters:
      - { name: jobId, in: path, required: true, schema: { type: string } }
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
