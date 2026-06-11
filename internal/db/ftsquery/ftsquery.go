// Package ftsquery builds safe FTS5 MATCH expressions from free text. It is
// shared by every feature that queries an FTS5 index (memory search, recally
// article search) so the escaping rules live in exactly one place.
package ftsquery

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuildMatchQuery converts free text into an FTS5 MATCH expression for a
// trigram-tokenized index. Tokens (runs of letters/digits/underscore) are
// individually quoted so FTS5 query operators in user input (*, -, :, parens)
// can never break the query, and OR-joined for recall — BM25 still ranks
// multi-term hits higher. Tokens shorter than 3 runes are dropped: the
// trigram tokenizer silently matches nothing for them, so they contribute
// zero recall. Returns "" when no usable token remains; callers must skip
// MATCH then (and may fall back to a LIKE scan, see EscapeLike).
func BuildMatchQuery(text string) string {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, `"`, "")
		if utf8.RuneCountInString(tok) < 3 {
			continue
		}
		quoted = append(quoted, `"`+tok+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// EscapeLike escapes LIKE wildcards (%, _) and the escape character itself so
// user text can be embedded in a `LIKE ? ESCAPE '\'` pattern as a literal
// substring.
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
