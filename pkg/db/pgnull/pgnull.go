// Package pgnull builds nullable pgx column values from plain Go values,
// collapsing the empty value to SQL NULL. It replaces the per-package
// `pgtype.Text{String: v, Valid: v != ""}` one-liners that accumulated across
// the codebase so every call site shares one NULL convention.
package pgnull

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// Text returns a pgtype.Text that is NULL when s is empty and the string
// otherwise. Use it for optional text columns where "" and NULL are the same
// absence (the common case: optional foreign keys, optional metadata).
func Text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// TextTrim is Text after trimming surrounding whitespace, so a value that is
// blank once trimmed also becomes NULL. Use it at ingestion boundaries where
// callers may pass whitespace-only strings.
func TextTrim(s string) pgtype.Text {
	return Text(strings.TrimSpace(s))
}
