package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Condition operators.
const (
	OpEq       = "eq"
	OpNeq      = "neq"
	OpIn       = "in"
	OpNotIn    = "not_in"
	OpContains = "contains"
)

// evaluateConditions parses the policy's conditions JSON and evaluates all
// conditions against the request. All conditions must be satisfied (AND).
//
// Conditions format:
//
//	{"resource.owner_id": {"eq": "subject.id"}, "resource.scope": {"eq": "system"}}
//
// Values can be attribute references (prefixed with "subject." or "resource.")
// or literal strings/numbers.
func evaluateConditions(conditionsJSON string, req AccessRequest) bool {
	if conditionsJSON == "" || conditionsJSON == "{}" {
		return true
	}

	var conditions map[string]map[string]any
	if err := json.Unmarshal([]byte(conditionsJSON), &conditions); err != nil {
		slog.Warn("policy engine: invalid conditions JSON", "json", conditionsJSON, "error", err)
		return false
	}

	if len(conditions) == 0 {
		return true
	}

	for attrPath, ops := range conditions {
		leftVal := resolveAttribute(attrPath, req)
		for op, rightRaw := range ops {
			if !evalOp(op, leftVal, rightRaw, req) {
				return false
			}
		}
	}

	return true
}

// resolveAttribute resolves an attribute path (e.g., "subject.id",
// "resource.owner_id", "resource.scope") to its string value from the
// request.
func resolveAttribute(path string, req AccessRequest) string {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) != 2 {
		return ""
	}

	prefix, field := parts[0], parts[1]

	switch prefix {
	case "subject":
		return resolveSubjectAttr(field, req.Subject)
	case "resource":
		return resolveResourceAttr(field, req.Resource)
	default:
		return ""
	}
}

// resolveSubjectAttr returns a string value for a subject attribute.
func resolveSubjectAttr(field string, s Subject) string {
	switch field {
	case "id":
		return strconv.FormatInt(s.UserID, 10)
	case "roles":
		// Return as JSON array for collection operators.
		b, _ := json.Marshal(s.Roles)
		return string(b)
	case "agent_ids":
		b, _ := json.Marshal(s.AgentIDs)
		return string(b)
	default:
		if s.Attrs != nil {
			return anyToString(s.Attrs[field])
		}
		return ""
	}
}

// resolveResourceAttr returns a string value for a resource attribute.
func resolveResourceAttr(field string, r Resource) string {
	switch field {
	case "type":
		return string(r.Type)
	case "id":
		return r.ID
	case "owner_id":
		return strconv.FormatInt(r.OwnerID, 10)
	default:
		if r.Attrs != nil {
			return anyToString(r.Attrs[field])
		}
		return ""
	}
}

// evalOp evaluates a single operator against left value and right operand.
// The right operand may be an attribute reference string or a literal.
func evalOp(op, leftVal string, rightRaw any, req AccessRequest) bool {
	switch op {
	case OpEq:
		rightVal := resolveOperand(rightRaw, req)
		return leftVal == rightVal

	case OpNeq:
		rightVal := resolveOperand(rightRaw, req)
		return leftVal != rightVal

	case OpIn:
		// leftVal should be a single value; right should resolve to a
		// collection (JSON array string or attribute reference to a list).
		collection := resolveCollection(rightRaw, req)
		for _, item := range collection {
			if leftVal == item {
				return true
			}
		}
		return false

	case OpNotIn:
		collection := resolveCollection(rightRaw, req)
		for _, item := range collection {
			if leftVal == item {
				return false
			}
		}
		return true

	case OpContains:
		// leftVal should resolve to a collection; right is a single value.
		leftCollection := resolveCollection(leftVal, req)
		rightVal := resolveOperand(rightRaw, req)
		for _, item := range leftCollection {
			if item == rightVal {
				return true
			}
		}
		return false

	default:
		slog.Warn("policy engine: unknown operator", "op", op)
		return false
	}
}

// resolveOperand resolves the right-hand side of an operator. If it is a
// string starting with "subject." or "resource.", it is treated as an
// attribute reference. Otherwise it is treated as a literal.
func resolveOperand(raw any, req AccessRequest) string {
	s, ok := raw.(string)
	if ok && isAttrRef(s) {
		return resolveAttribute(s, req)
	}
	return anyToString(raw)
}

// resolveCollection resolves an operand to a string slice. It handles:
//   - attribute reference strings (e.g., "subject.agent_ids") that resolve
//     to a JSON array
//   - JSON array strings (e.g., '["a","b"]')
//   - plain any values that are already slices
func resolveCollection(raw any, req AccessRequest) []string {
	// If raw is a string, check if it's an attribute ref first.
	if s, ok := raw.(string); ok {
		resolved := s
		if isAttrRef(s) {
			resolved = resolveAttribute(s, req)
		}
		// Try to parse as JSON array.
		var arr []any
		if json.Unmarshal([]byte(resolved), &arr) == nil {
			result := make([]string, len(arr))
			for i, v := range arr {
				result[i] = anyToString(v)
			}
			return result
		}
		// Single value.
		return []string{resolved}
	}

	// If raw is already a slice (from JSON unmarshal).
	if arr, ok := raw.([]any); ok {
		result := make([]string, len(arr))
		for i, v := range arr {
			result[i] = anyToString(v)
		}
		return result
	}

	return nil
}

// isAttrRef returns true if the string looks like an attribute reference.
func isAttrRef(s string) bool {
	return strings.HasPrefix(s, "subject.") || strings.HasPrefix(s, "resource.")
}

// anyToString converts an arbitrary value to its string representation.
func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	case bool:
		return strconv.FormatBool(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
