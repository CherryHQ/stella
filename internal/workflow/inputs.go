package workflow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CherryHQ/stella/internal/goal"
)

type InputSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

var inputPlaceholderRE = regexp.MustCompile(`\{\{\s*inputs\.([A-Za-z0-9_\-]+)\s*\}\}`)

func ResolveInputs(specs []InputSpec, provided map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(specs))
	known := make(map[string]InputSpec, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, fmt.Errorf("workflow input: empty name")
		}
		if _, ok := known[name]; ok {
			return nil, fmt.Errorf("workflow input %q: duplicate", name)
		}
		known[name] = spec
		if spec.Default != "" {
			resolved[name] = spec.Default
		}
	}
	for name, value := range provided {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("workflow input %q: unknown", name)
		}
		resolved[name] = value
	}
	for name, spec := range known {
		if spec.Required {
			if _, ok := resolved[name]; !ok {
				return nil, fmt.Errorf("workflow input %q: required", name)
			}
		}
	}
	return resolved, nil
}

func SubstituteInputs(plan FrozenPlan, inputs map[string]string) (FrozenPlan, error) {
	for i := range plan.Children {
		child, err := substituteChild(plan.Children[i].Child, inputs)
		if err != nil {
			return FrozenPlan{}, err
		}
		plan.Children[i].Child = child
		if plan.Children[i].Plan != nil {
			nested, err := SubstituteInputs(*plan.Children[i].Plan, inputs)
			if err != nil {
				return FrozenPlan{}, err
			}
			plan.Children[i].Plan = &nested
		}
	}
	return plan, nil
}

func substituteChild(ch goal.ProposedChild, inputs map[string]string) (goal.ProposedChild, error) {
	var err error
	if ch.Title, err = substituteText(ch.Title, inputs); err != nil {
		return ch, fmt.Errorf("child %q title: %w", ch.Key, err)
	}
	if ch.Intent, err = substituteText(ch.Intent, inputs); err != nil {
		return ch, fmt.Errorf("child %q intent: %w", ch.Key, err)
	}
	for i := range ch.AcceptanceContract.Items {
		item := &ch.AcceptanceContract.Items[i]
		if item.Prompt, err = substituteText(item.Prompt, inputs); err != nil {
			return ch, fmt.Errorf("child %q prompt: %w", ch.Key, err)
		}
		if item.Rubric, err = substituteText(item.Rubric, inputs); err != nil {
			return ch, fmt.Errorf("child %q rubric: %w", ch.Key, err)
		}
	}
	return ch, nil
}

func substituteText(s string, inputs map[string]string) (string, error) {
	var err error
	out := inputPlaceholderRE.ReplaceAllStringFunc(s, func(m string) string {
		if err != nil {
			return m
		}
		parts := inputPlaceholderRE.FindStringSubmatch(m)
		name := parts[1]
		value, ok := inputs[name]
		if !ok {
			err = fmt.Errorf("unknown placeholder %q", name)
			return m
		}
		return value
	})
	if err != nil {
		return "", err
	}
	if strings.Contains(out, "{{inputs.") {
		return "", fmt.Errorf("unresolved input placeholder")
	}
	return out, nil
}
