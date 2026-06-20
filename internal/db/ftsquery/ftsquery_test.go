package ftsquery

import "testing"

func TestBuildMatchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"hello", `"hello"`},
		{"hello world", `"hello" OR "world"`},
		{"snake_case token2", `"snake_case" OR "token2"`},
		{`quoted "phrase" here`, `"quoted" OR "phrase" OR "here"`},
		{"日本語 test", `"test"`},
		// tsquery operators and punctuation are separators, never query syntax.
		{"a* (b) -c:d", ""},
		{"", ""},
		{`*** (") -:`, ""},
		{"   ", ""},
		// Tokens under 3 runes, and any token with CJK characters, can't become
		// a 'simple'-parser lexeme the tsvector index matches, so they are
		// dropped entirely; the dropped remainder routes to the pg_trgm LIKE
		// fallback. Mixed queries keep only the usable (non-CJK, 3+ rune) tokens.
		{"部署", ""},
		{"go", ""},
		{"ai 部署", ""},
		{"部署方案", ""},
		{"go 部署 deployment", `"deployment"`},
		{"部署 部署方案", ""},
	}
	for _, tc := range tests {
		if got := BuildMatchQuery(tc.input); got != tc.want {
			t.Errorf("BuildMatchQuery(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"plain", "plain"},
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
		{"部署", "部署"},
	}
	for _, tc := range tests {
		if got := EscapeLike(tc.input); got != tc.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
