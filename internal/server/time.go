package server

import (
	"time"
)

// parseTime parses a domain-layer timestamp string (string-typed fields on
// non-sqlc types such as memory.ConstraintEntry and vault.EntryMeta) into a UTC
// time.Time. Accepts RFC3339(Nano) or the legacy naive "2006-01-02 15:04:05"
// form. Empty or unparseable input yields the zero time.
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
