package goal

import (
	"encoding/json"
	"testing"
)

func TestGoalControlSchemasMarshalAndPinDriftFields(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
	}{
		{name: "execute", schema: goalControlExecuteInputSchema()},
		{name: "decompose", schema: goalControlDecomposeInputSchema()},
		{name: "review", schema: goalControlReviewInputSchema()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.schema)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("empty schema JSON")
			}
			if containsKey(tt.schema, "$ref") {
				t.Fatal("generated tool schema must be dereferenced")
			}
		})
	}

	decompose := goalControlDecomposeInputSchema()
	acceptanceItem := schemaAt(t, decompose, "properties", "decomposition", "properties", "children", "items", "properties", "acceptance_contract", "properties", "items", "items", "properties")
	if _, ok := acceptanceItem["expect_exit"]; !ok {
		t.Fatal("decomposition acceptance item schema missing expect_exit")
	}

	convergence := schemaAt(t, decompose, "properties", "decomposition", "properties", "children", "items", "properties", "convergence_policy", "properties")
	for _, field := range []string{"planner_repair_max", "max_concurrent"} {
		if _, ok := convergence[field]; !ok {
			t.Fatalf("convergence_policy missing %s", field)
		}
	}

	reviewPolicy := schemaAt(t, decompose, "properties", "decomposition", "properties", "children", "items", "properties", "review_policy")
	if !enumHas(reviewPolicy, "none") || !enumHas(reviewPolicy, "human") {
		t.Fatalf("review_policy enum = %#v, want none and human", reviewPolicy["enum"])
	}

	executeAction := schemaAt(t, goalControlExecuteInputSchema(), "properties", "action")
	for _, action := range []string{"submit", "fail"} {
		if !enumHas(executeAction, action) {
			t.Fatalf("execute action enum missing %q: %#v", action, executeAction["enum"])
		}
	}
	if enumHas(executeAction, "block") {
		t.Fatalf("execute action enum still exposes block: %#v", executeAction["enum"])
	}

	review := goalControlReviewInputSchema()
	verdict := schemaAt(t, review, "properties", "verdicts", "items", "properties")
	for _, field := range []string{"item_id", "pass", "rationale"} {
		if _, ok := verdict[field]; !ok {
			t.Fatalf("review verdict missing %s", field)
		}
	}
}

func schemaAt(t *testing.T, schema map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := any(schema)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("schema path %v: %q parent is %T", path, key, cur)
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("schema path %v missing %q", path, key)
		}
	}
	out, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("schema path %v is %T, want object", path, cur)
	}
	return out
}

func enumHas(schema map[string]any, want string) bool {
	items, ok := schema["enum"].([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if k == key || containsKey(item, key) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if containsKey(item, key) {
				return true
			}
		}
	}
	return false
}
