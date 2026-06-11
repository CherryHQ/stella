// Package ftsquery builds safe FTS5 MATCH expressions from free text. It is
// shared by every feature that queries an FTS5 index (memory search, recally
// article search) so the escaping rules live in exactly one place.
package ftsquery

import (
	"strings"
	"unicode"
)

// BuildMatchQuery converts free text into an FTS5 MATCH expression. Tokens
// (runs of letters/digits/underscore) are individually quoted so FTS5 query
// operators in user input (*, -, :, parens) can never break the query, and
// OR-joined for recall — BM25 still ranks multi-term hits higher. Returns ""
// when no tokens can be extracted; callers must skip the query then.
func BuildMatchQuery(text string) string {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, `"`, "")
		if tok == "" {
			continue
		}
		quoted = append(quoted, `"`+tok+`"`)
	}
	return strings.Join(quoted, " OR ")
}
