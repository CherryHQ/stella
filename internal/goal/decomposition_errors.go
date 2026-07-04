package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ValidationError is a structured planner-contract violation that can be fed
// back to the same planning session without asking the model to infer from prose.
type ValidationError struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

func RenderErrorsJSON(errs []ValidationError) string {
	b, err := json.MarshalIndent(errs, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}

func RenderErrorsText(errs []ValidationError) string {
	if len(errs) == 0 {
		return "valid"
	}
	var b strings.Builder
	for _, e := range errs {
		fmt.Fprintf(&b, "- %s at %s: %s", e.Code, e.Path, e.Message)
		if e.Expected != "" {
			fmt.Fprintf(&b, " (expected: %s)", e.Expected)
		}
		if e.Actual != "" {
			fmt.Fprintf(&b, " (actual: %s)", e.Actual)
		}
		if e.Hint != "" {
			fmt.Fprintf(&b, " hint: %s", e.Hint)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func validateDecompositionDetailed(c DecompositionContent, parentDepth, maxDepth int) []ValidationError {
	var errs []ValidationError
	add := func(path, code, message, expected, actual, hint string) {
		errs = append(errs, ValidationError{Path: path, Code: code, Message: message, Expected: expected, Actual: actual, Hint: hint})
	}

	if len(c.Children) > maxDecompositionBreadth {
		add("/children", "too_many_children", "decomposition has too many children", fmt.Sprintf("at most %d", maxDecompositionBreadth), fmt.Sprintf("%d", len(c.Children)), "merge or stage lower-priority child goals")
	}
	if parentDepth+1 > maxDepth {
		add("/children", "depth_exceeded", "child depth would exceed max_depth", fmt.Sprintf("parent_depth+1 <= %d", maxDepth), fmt.Sprintf("%d", parentDepth+1), "make this goal a leaf or raise max_depth before planning")
	}

	keys := make(map[string]ProposedChild, len(c.Children))
	requiredCount := 0
	for i, ch := range c.Children {
		base := fmt.Sprintf("/children/%d", i)
		if ch.Key == "" {
			add(base+"/key", "missing_child_key", "child key must be non-empty", "stable non-empty key", "", "generate a short slug and use it in edges")
		} else if _, dup := keys[ch.Key]; dup {
			add(base+"/key", "duplicate_child_key", fmt.Sprintf("duplicate child key %q", ch.Key), "unique child keys", ch.Key, "rename this child and update any edges")
		}
		if ch.Kind != "" && !ValidKind(ch.Kind) {
			add(base+"/kind", "invalid_child_kind", "child kind is not supported", "leaf or composite", ch.Kind, "choose leaf unless the child needs its own decomposition")
		}
		if !ch.AcceptanceContract.Valid() {
			add(base+"/acceptance_contract", "invalid_acceptance_contract", "acceptance contract is invalid", "valid Stella acceptance contract", string(marshalJSON(ch.AcceptanceContract)), "fix policy/items or use an empty object for trivial acceptance")
		}
		if !ch.ConvergencePolicy.Valid() {
			add(base+"/convergence_policy", "invalid_convergence_policy", "convergence policy is invalid", "non-negative bounds and known escalation", string(marshalJSON(ch.ConvergencePolicy)), "remove negative values and use escalation block or abandon")
		}
		if ch.Kind == KindComposite {
			if ch.AcceptanceContract.HasDeterministicItem() {
				add(base+"/acceptance_contract", "composite_deterministic_contract", "composite children cannot carry deterministic acceptance items", "judgment-only or trivial composite contract", string(marshalJSON(ch.AcceptanceContract)), "put deterministic checks on leaf children")
			}
			if parentDepth+2 > maxDepth {
				add(base+"/kind", "depth_exceeded", "composite child would have no depth headroom for its own children", fmt.Sprintf("parent_depth+2 <= %d", maxDepth), fmt.Sprintf("%d", parentDepth+2), "make this child a leaf or reduce nesting")
			}
		}
		if ch.ReviewPolicy != "" && !ValidReviewPolicy(ch.ReviewPolicy) {
			add(base+"/review_policy", "invalid_review_policy", "review policy is not supported", "none or human", ch.ReviewPolicy, "omit review_policy or set it to human")
		}
		if ch.Required {
			requiredCount++
		}
		if ch.Key != "" {
			keys[ch.Key] = ch
		}
	}
	if requiredCount < 1 {
		add("/children", "no_required_child", "decomposition must include at least one required child", "at least one child with required=true", "0", "mark the child that completes the goal as required")
	}

	adj := make(map[string][]string, len(c.Children))
	for i, e := range c.Edges {
		base := fmt.Sprintf("/edges/%d", i)
		if _, ok := keys[e.DownstreamKey]; !ok {
			add(base+"/downstream_key", "unknown_edge_downstream", fmt.Sprintf("downstream key %q does not match any child", e.DownstreamKey), oneOfChildKeys(keys), e.DownstreamKey, "use an existing children[].key")
		}
		if _, ok := keys[e.UpstreamKey]; !ok {
			add(base+"/upstream_key", "unknown_edge_upstream", fmt.Sprintf("upstream key %q does not match any child", e.UpstreamKey), oneOfChildKeys(keys), e.UpstreamKey, "use an existing children[].key")
		}
		if e.DownstreamKey != "" && e.DownstreamKey == e.UpstreamKey {
			add(base, "self_dependency", "edge cannot point a child at itself", "different upstream and downstream keys", e.DownstreamKey, "remove the edge or choose a different upstream")
		}
		if e.Kind != "" && !ValidEdgeKind(e.Kind) {
			add(base+"/kind", "invalid_edge_kind", "edge kind is not supported", "hard or soft", e.Kind, "use hard for blocking dependencies or soft for advisory ordering")
		}
		if e.OnFailure != "" && !ValidOnFailure(e.OnFailure) {
			add(base+"/on_failure", "invalid_edge_on_failure", "edge on_failure policy is not supported", "block, fail, or ignore", e.OnFailure, "choose how downstream should react if upstream fails")
		}
		if _, ok := keys[e.DownstreamKey]; ok {
			if _, ok := keys[e.UpstreamKey]; ok && e.DownstreamKey != e.UpstreamKey {
				adj[e.UpstreamKey] = append(adj[e.UpstreamKey], e.DownstreamKey)
			}
		}
	}
	if len(errs) == 0 && hasCycleDFS(keys, adj) {
		add("/edges", "cycle_detected", "dependency edges contain a cycle", "DAG", "cycle", "remove an edge or split the loop into a child goal")
	}
	return errs
}

func validateDecompositionError(c DecompositionContent, parentDepth, maxDepth int) error {
	errs := validateDecompositionDetailed(c, parentDepth, maxDepth)
	if len(errs) == 0 {
		return nil
	}
	return validationErrorClass(errs[0])
}

func deterministicCapabilityErrors(c DecompositionContent) []ValidationError {
	var errs []ValidationError
	for i, ch := range c.Children {
		kind := ch.Kind
		if kind == "" {
			kind = KindLeaf
		}
		if kind != KindLeaf || !ch.AcceptanceContract.HasRequiredDeterministicItem() {
			continue
		}
		errs = append(errs, ValidationError{
			Path:     fmt.Sprintf("/children/%d/acceptance_contract", i),
			Code:     "deterministic_checks_unsupported",
			Message:  "required deterministic acceptance checks cannot run on this deployment",
			Expected: "judgment acceptance or a sandbox-capable backend",
			Actual:   string(marshalJSON(ch.AcceptanceContract)),
			Hint:     "remove required deterministic items or enable a sandbox backend before planning",
		})
	}
	return errs
}

func validationErrorClass(e ValidationError) error {
	switch e.Code {
	case "invalid_acceptance_contract", "invalid_convergence_policy":
		return ErrInvalidContract
	case "composite_deterministic_contract":
		return ErrCompositeDeterministicContract
	case "deterministic_checks_unsupported":
		return ErrDeterministicChecksUnsupported
	case "depth_exceeded":
		return ErrDepthExceeded
	case "self_dependency", "cycle_detected":
		return ErrCycle
	default:
		return ErrInvalidDecomposition
	}
}

func structuralValidationErrors(err error) []ValidationError {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidDecomposition) || errors.Is(err, ErrInvalidContract) ||
		errors.Is(err, ErrCompositeDeterministicContract) || errors.Is(err, ErrDeterministicChecksUnsupported) ||
		errors.Is(err, ErrDepthExceeded) || errors.Is(err, ErrCycle) {
		return []ValidationError{{Path: "/", Code: "structural_error", Message: err.Error(), Hint: "fix the decomposition and call goal_control again"}}
	}
	return nil
}

func oneOfChildKeys(keys map[string]ProposedChild) string {
	if len(keys) == 0 {
		return "no child keys available"
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
