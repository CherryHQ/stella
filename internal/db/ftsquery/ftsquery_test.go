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
		{"日本語 test", `"日本語" OR "test"`},
		// FTS5 operators and punctuation are separators, never query syntax.
		{"a* (b) -c:d", ""},
		{"", ""},
		{`*** (") -:`, ""},
		{"   ", ""},
		// Tokens under 3 runes match nothing on a trigram index, so they are
		// dropped entirely; mixed queries keep only the usable tokens.
		{"部署", ""},
		{"go", ""},
		{"ai 部署", ""},
		{"部署方案", `"部署方案"`},
		{"go 部署 deployment", `"deployment"`},
		{"部署 部署方案", `"部署方案"`},
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
