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
func ParseTimeToUnixMs(s string) (int64, error) {
	unix, err := ParseTimeToUnix(s)
	if err != nil {
		return 0, err
	}
	return unix * 1000, nil
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
