package feishutool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// ParseTimeToUnix parses an ISO 8601 time string to Unix seconds.
// Accepts formats: "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02".
// Falls back to parsing as a raw Unix timestamp string.
func ParseTimeToUnix(s string) (int64, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	// Try raw numeric.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	return 0, fmt.Errorf("feishutool: cannot parse time %q", s)
}

// ParseTimeToUnixMs parses an ISO 8601 time string to Unix milliseconds.
// Note: if the input is a raw numeric string, ParseTimeToUnix treats it as
// seconds, so this function returns that value * 1000. If you already have
// milliseconds as a raw string, convert directly with strconv.ParseInt.
func ParseTimeToUnixMs(s string) (int64, error) {
	unix, err := ParseTimeToUnix(s)
	if err != nil {
		return 0, err
	}
	return unix * 1000, nil
}

// intArg extracts an integer argument from a tool args map.
// Returns 0 if the key is missing or not a numeric type.
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

// boolArg extracts a boolean argument from a tool args map.
func boolArg(args map[string]any, key string) (bool, bool) {
	v, ok := args[key].(bool)
	return v, ok
}

// mapArg extracts a map argument from a tool args map.
func mapArg(args map[string]any, key string) map[string]any {
	m, _ := args[key].(map[string]any)
	return m
}

// sliceArg extracts a slice argument from a tool args map.
func sliceArg(args map[string]any, key string) []any {
	s, _ := args[key].([]any)
	return s
}

// FormatLarkError formats a Lark API error into a human-readable string.
func FormatLarkError(code int, msg string) string {
	return fmt.Sprintf("Feishu API error (code=%d): %s", code, msg)
}

// PaginatedResult holds a page of results with an optional continuation token.
type PaginatedResult[T any] struct {
	Items     []T    `json:"items"`
	PageToken string `json:"page_token,omitempty"`
	HasMore   bool   `json:"has_more"`
	Total     int    `json:"total,omitempty"`
}

// JSONResult builds a JSON string from a map for tool return values.
func JSONResult(data map[string]any) (string, error) {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("feishutool: marshal result: %w", err)
	}
	return string(out), nil
}

// JSONResultFromAny builds a JSON string from any serializable value.
func JSONResultFromAny(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("feishutool: marshal result: %w", err)
	}
	return string(out), nil
}

// stringArg extracts a string argument from a tool args map.
func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// derefStr safely dereferences a string pointer.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt safely dereferences an int pointer.
func derefInt(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// toStringSlice extracts a string slice from a tool args map.
func toStringSlice(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return s
	default:
		return nil
	}
}

// mustParseSchema parses a JSON schema string into a map, panicking on error.
// Used for package-level schema initialization where invalid JSON is a bug.
func mustParseSchema(jsonStr string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		panic(fmt.Sprintf("feishutool: invalid schema JSON: %v", err))
	}
	return m
}

// paginatedResultMap builds a standard paginated result map from Lark SDK
// pagination fields. Reduces boilerplate across tool implementations.
func paginatedResultMap(key string, items any, hasMore *bool, pageToken *string) map[string]any {
	hm := hasMore != nil && *hasMore
	pt := ""
	if pageToken != nil {
		pt = *pageToken
	}
	return map[string]any{
		key:          items,
		"has_more":   hm,
		"page_token": pt,
	}
}
