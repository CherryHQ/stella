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
		{"a* (b) -c:d", `"a" OR "b" OR "c" OR "d"`},
		{"", ""},
		{`*** (") -:`, ""},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := BuildMatchQuery(tc.input); got != tc.want {
			t.Errorf("BuildMatchQuery(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
