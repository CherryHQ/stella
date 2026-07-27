package scheduler

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/recally"
)

// The builtin job templates are prompts stored as Go strings. They tell a
// worker to call the native recally tool with specific `action=<token>` verbs
// and arguments. Those strings drifted from the tool surface once already
// (they instructed a deleted CLI and named a tool action that did not exist),
// silently breaking every scheduled RSS/digest run.
//
// This test makes that whole bug class impossible: it mechanically extracts
// every `action=` verb and every `<name>=` argument from each template and
// asserts them against the recally tool's real generated input schema. Add
// `action=does_not_exist` or a typo like `limitt=20` to a template and this
// test fails, naming the offending template key and token.

// tokenAssign matches `<word>=` assignments (action=feed_poll, limit=20).
var tokenAssign = regexp.MustCompile(`(\w+)=(\w+)?`)

// recallyTemplates maps each builtin template to the tool whose action/argument
// surface it drives. Both current builtins speak only the recally tool, so the
// mapping is hard-coded (there is no builtin-template registry to iterate); add
// new entries here when a recally-driven builtin is introduced.
func recallyTemplates() map[string]JobTemplate {
	return map[string]JobTemplate{
		RecallyRSSTemplate.Key:    RecallyRSSTemplate,
		RecallyDigestTemplate.Key: RecallyDigestTemplate,
	}
}

// recallyActionProps maps each recally action to its declared property names.
// Actions come from the schema's `action` enum; per-action properties come from
// the json tags of the generated Handler input structs — the exact fields
// Dispatch/DecodeInput accept at runtime (the wire schema no longer carries
// per-action variants; OpenAI-compatible providers reject top-level oneOf).
func recallyActionProps(t *testing.T) map[string]map[string]bool {
	t.Helper()
	schema := recally.InputSchema()
	actionDef, ok := schema["properties"].(map[string]any)["action"].(map[string]any)
	if !ok {
		t.Fatalf("recally InputSchema has no action property: %#v", schema["properties"])
	}
	enum, ok := actionDef["enum"].([]any)
	if !ok || len(enum) == 0 {
		t.Fatalf("recally action property has no enum: %#v", actionDef)
	}
	handler := reflect.TypeFor[recally.Handler]()
	out := map[string]map[string]bool{}
	for _, raw := range enum {
		action, ok := raw.(string)
		if !ok {
			t.Fatalf("action enum value is not a string: %#v", raw)
		}
		// Mirror toolgen's exportName: snake_case action -> Handler method name.
		// If toolgen's naming ever changes, MethodByName fails loudly here.
		method, ok := handler.MethodByName(exportActionName(action))
		if !ok {
			t.Fatalf("recally.Handler has no method for action %q (looked for %q)", action, exportActionName(action))
		}
		in := method.Type.In(1)
		set := map[string]bool{"action": true}
		for i := range in.NumField() {
			name, _, _ := strings.Cut(in.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				set[name] = true
			}
		}
		out[action] = set
	}
	return out
}

func exportActionName(action string) string {
	parts := strings.Split(action, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func TestBuiltinTemplatesMatchRecallySchema(t *testing.T) {
	actionProps := recallyActionProps(t)

	for key, tmpl := range recallyTemplates() {
		// Scan assignments left-to-right, tracking the action each argument
		// belongs to: an `action=X` sets the current action, and every later
		// `<name>=` belongs to it until the next `action=`.
		var current string
		for _, m := range tokenAssign.FindAllStringSubmatch(tmpl.Message, -1) {
			name, value := m[1], m[2]
			if name == "action" {
				if _, ok := actionProps[value]; !ok {
					t.Errorf("template %q references unknown recally action %q", key, value)
				}
				current = value
				continue
			}
			if current == "" {
				t.Errorf("template %q uses argument %q before naming an action", key, name)
				continue
			}
			props, ok := actionProps[current]
			if !ok {
				// Already reported the bad action above; skip arg checks for it.
				continue
			}
			if !props[name] {
				t.Errorf("template %q passes argument %q to recally action %q, which has no such property", key, name, current)
			}
		}
	}
}
