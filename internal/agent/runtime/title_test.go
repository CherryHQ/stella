package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAutoTitleReadsJSONPayloads(t *testing.T) {
	// A webhook posts a body, and the body becomes the session's name. Left
	// alone these render in every thread list as a raw fragment.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"label and body", `{"event":"deploy","message":"shipped v2"}`, "deploy · shipped v2"},
		{"body only", `{"message":"disk almost full"}`, "disk almost full"},
		{"label only", `{"event":"heartbeat"}`, "heartbeat"},
		{"array payload", `[{"event":"batch","message":"3 items"}]`, "batch · 3 items"},
		{"nested body", `{"meta":{"id":7},"event":"push","message":"main"}`, "push · main"},
		{
			"prefers the more specific label key",
			`{"type":"generic","event":"push","text":"main"}`,
			"push · main",
		},
		{"case-insensitive keys", `{"Event":"Deploy","Message":"ok"}`, "Deploy · ok"},
		{
			// A GitHub webhook says what happened at the top level and describes
			// the actor underneath. Ranking by key name alone titles it after
			// the sender, because `type` outranks `action` in titleLabelKeys.
			"a top-level label outranks a better-named nested one",
			`{"action":"opened","sender":{"type":"User"},"message":"pull request"}`,
			"opened · pull request",
		},
		{
			// The empty string is a legal key. Treating it as "no key yet"
			// shifts the walker by one and pairs up the *keys* that follow.
			"an empty key does not desynchronize the walker",
			`{"":"noise","event":"deploy","message":"ok"}`,
			"deploy · ok",
		},
		{"unescapes and flattens", `{"event":"ci","message":"line one\nline two"}`, "ci · line one line two"},
		{"skips empty values", `{"event":"","message":"only a body"}`, "only a body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoTitle(tc.in); got != tc.want {
				t.Fatalf("autoTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAutoTitleFallsBackToTheRawText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"prose", "what a good day", "what a good day"},
		{"empty", "", ""},
		{"a brace in a sentence is not a payload", `use the {"a":1} syntax`, `use the {"a":1} syntax`},
		{"file upload marker", "[file: /user/assets/report.pdf]", "[file: /user/assets/report.pdf]"},
		{"no string values to show", `{"count":42,"ok":true}`, `{"count":42,"ok":true}`},
		{"malformed json", `{"event":"x"`, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoTitle(tc.in); got != tc.want {
				t.Fatalf("autoTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAutoTitleKeepsAKeyOrderThatDoesNotDependOnMapIteration(t *testing.T) {
	// With no recognized key the first two values are shown, so the walk has to
	// preserve document order. Decoding into a map would shuffle it per run and
	// give the same payload two different titles.
	const payload = `{"alpha":"one","bravo":"two","charlie":"three"}`
	first := autoTitle(payload)
	if first != "one · two" {
		t.Fatalf("autoTitle = %q, want %q", first, "one · two")
	}
	for range 50 {
		if got := autoTitle(payload); got != first {
			t.Fatalf("autoTitle is not deterministic: got %q then %q", first, got)
		}
	}
}

func TestAutoTitleTruncatesOnRuneBoundaries(t *testing.T) {
	// The old implementation sliced bytes. Chinese has no spaces to break on, so
	// it always fell through to the hard cut and stored invalid UTF-8.
	long := strings.Repeat("你好世界", 30)
	got := autoTitle(long)

	if !utf8.ValidString(got) {
		t.Fatalf("autoTitle produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("autoTitle produced a replacement character: %q", got)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n > titleMaxRunes {
		t.Fatalf("autoTitle kept %d runes, want <= %d", n, titleMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("autoTitle did not mark the cut: %q", got)
	}
}

func TestAutoTitleTruncatesAtAWordBoundaryWhenThereIsOne(t *testing.T) {
	in := strings.Repeat("alpha bravo ", 20)
	got := autoTitle(in)

	if !strings.HasSuffix(got, "…") {
		t.Fatalf("autoTitle did not mark the cut: %q", got)
	}
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Fatalf("autoTitle left a trailing space before the ellipsis: %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if utf8.RuneCountInString(body) > titleMaxRunes {
		t.Fatalf("autoTitle kept %d runes, want <= %d", utf8.RuneCountInString(body), titleMaxRunes)
	}
	if !strings.HasPrefix(in, body) {
		t.Fatalf("autoTitle changed the text it kept: %q", body)
	}
}

func TestAutoTitleTruncatesAnExtractedTitleToo(t *testing.T) {
	// Extraction happens first, so a long body still has to be bounded.
	in := `{"event":"deploy","message":"` + strings.Repeat("x", 200) + `"}`
	got := autoTitle(in)

	if n := utf8.RuneCountInString(got); n > titleMaxRunes+1 { // +1 for the ellipsis
		t.Fatalf("autoTitle returned %d runes: %q", n, got)
	}
	if !strings.HasPrefix(got, "deploy · ") {
		t.Fatalf("autoTitle dropped the label: %q", got)
	}
}
