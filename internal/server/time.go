package server

import (
	"database/sql"
	"fmt"
	"time"
)

// parseTime parses a database timestamp string into a UTC time.Time. SQLite
// columns default to the naive "2006-01-02 15:04:05" form (UTC, no zone
// marker); callers hand the result to encoding/json, which serializes it as
// RFC3339 with a zone so clients don't misread it as local time. Empty or
// unparseable input yields the zero time.
func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseTimePtr is the nullable variant of parseTime: an invalid NullString or
// unparseable value yields nil rather than the zero time.
func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid {
		return nil
	}
	t := parseTime(ns.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func parseSQLValueTime(value any) time.Time {
	switch v := value.(type) {
	case nil:
		return time.Time{}
	case string:
		return parseTime(v)
	case []byte:
		return parseTime(string(v))
	case time.Time:
		return v.UTC()
	default:
		return parseTime(fmt.Sprint(v))
	}
}
