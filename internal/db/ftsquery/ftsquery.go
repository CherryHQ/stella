// Package ftsquery builds safe FTS5 MATCH expressions from free text. It is
// shared by every feature that queries an FTS5 index (memory search, recally
// article search) so the escaping rules live in exactly one place.
package ftsquery

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuildMatchQuery converts free text into a websearch_to_tsquery argument for
// the 'simple'-config tsvector columns (search_tsv / content_tsv). Each token
// (a run of letters/digits/underscore) is wrapped in double quotes so that
// websearch_to_tsquery reads it as a literal phrase — user input can never
// inject tsquery operators — and tokens are OR-joined for recall, with
// ts_rank_cd still ranking multi-term hits higher. Two kinds of token are
// dropped because the 'simple' parser cannot turn them into a lexeme the index
// will match: tokens shorter than 3 runes, and tokens containing CJK characters
// (the parser does not segment space-less CJK). Callers route the dropped
// remainder to the pg_trgm LIKE fallback instead (see EscapeLike). Returns ""
// when no usable token remains, signalling callers to skip the tsvector MATCH.
func BuildMatchQuery(text string) string {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, `"`, "")
		if utf8.RuneCountInString(tok) < 3 || hasCJK(tok) {
			continue
		}
		quoted = append(quoted, `"`+tok+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// hasCJK reports whether s contains a Han, Kana, or Hangul character. The
// 'simple' text-search parser does not segment space-less CJK into lexemes, so
// such text never matches via the tsvector index; callers fall back to a
// pg_trgm substring scan for it.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// EscapeLike escapes LIKE wildcards (%, _) and the escape character itself so
// user text can be embedded in a `LIKE ? ESCAPE '\'` pattern as a literal
// substring.
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
