package workflow

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/goal"
)

func TestResolveInputsAndSubstitution(t *testing.T) {
	resolved, err := ResolveInputs([]InputSpec{{Name: "topic", Required: true}, {Name: "audience", Default: "team"}}, map[string]string{"topic": "release"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved["audience"] != "team" {
		t.Fatalf("default not applied")
	}
	if _, err := ResolveInputs([]InputSpec{{Name: "topic", Required: true}}, nil); err == nil {
		t.Fatalf("missing required should fail")
	}
	if _, err := ResolveInputs([]InputSpec{{Name: "topic"}}, map[string]string{"other": "x"}); err == nil {
		t.Fatalf("unknown provided input should fail")
	}

	plan := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "a", Title: "Write {{inputs.topic}}", Intent: "for {{ inputs.audience }}", Kind: goal.KindLeaf, Required: true, AcceptanceContract: goal.AcceptanceContract{Policy: goal.PolicyAll, Items: []goal.AcceptanceItem{{ID: "j", Kind: goal.ItemJudgment, Required: true, Authority: goal.AuthorityHuman, Prompt: "approve {{inputs.topic}}", Rubric: "rubric {{inputs.audience}}"}, {ID: "d", Kind: goal.ItemDeterministic, Required: false, Command: "echo {{inputs.topic}}"}}}}}}}
	sub, err := SubstituteInputs(plan, resolved)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	child := sub.Children[0].Child
	if child.Title != "Write release" || child.Intent != "for team" || child.AcceptanceContract.Items[0].Prompt != "approve release" {
		t.Fatalf("unexpected substitution: %+v", child)
	}
	if !strings.Contains(child.AcceptanceContract.Items[1].Command, "{{inputs.topic}}") {
		t.Fatalf("command must not be substituted")
	}

	unknown := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "a", Title: "{{inputs.nope}}", Kind: goal.KindLeaf, Required: true}}}}
	if _, err := SubstituteInputs(unknown, resolved); err == nil {
		t.Fatalf("unknown placeholder should fail")
	}
	unresolved := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "a", Title: "{{inputs.topic", Kind: goal.KindLeaf, Required: true}}}}
	if _, err := SubstituteInputs(unresolved, resolved); err == nil {
		t.Fatalf("unresolved placeholder should fail")
	}
}

func TestValidateSpecsAndPlaceholders(t *testing.T) {
	if err := ValidateSpecs([]InputSpec{{Name: "topic"}, {Name: "a-b_1"}}); err != nil {
		t.Fatalf("valid specs rejected: %v", err)
	}
	for _, bad := range [][]InputSpec{
		{{Name: "has space"}},
		{{Name: ""}},
		{{Name: "topic"}, {Name: "topic"}},
	} {
		if err := ValidateSpecs(bad); err == nil {
			t.Fatalf("specs %+v should be rejected", bad)
		}
	}
	if err := ValidatePlaceholders([]InputSpec{{Name: "topic"}}, `plan {{inputs.topic}}`, "intent {{ inputs.topic }}"); err != nil {
		t.Fatalf("declared placeholder rejected: %v", err)
	}
	if err := ValidatePlaceholders([]InputSpec{{Name: "topic"}}, `plan {{inputs.topci}}`); err == nil {
		t.Fatalf("undeclared placeholder should be rejected")
	}
}
