package server

import "time"

// parseTime normalizes a database timestamp string to RFC3339 (UTC) for API
// responses. SQLite columns default to the naive "2006-01-02 15:04:05" form
// (UTC, no zone marker); emitting that verbatim makes browsers parse it as
// local time. Parsing it as UTC and re-emitting with a zone fixes that.
// Empty input (the absent/null case) returns ""; already-RFC3339 and
// unparseable values pass through unchanged.
func parseTime(value string) string {
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return value
}
