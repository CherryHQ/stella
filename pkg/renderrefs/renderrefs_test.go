package renderrefs

import (
	"strings"
	"testing"
)

func TestEmitExtractRoundTrip(t *testing.T) {
	var b strings.Builder
	ref := Reference{Type: "task", ID: "abc", Intent: "created", Preview: &Preview{Title: "Fix login", Status: "draft"}}
	if err := Emit(&b, ref); err != nil {
		t.Fatalf("emit: %v", err)
	}
	// Sentinel interleaved with ordinary tool output and a tail marker.
	text := "creating task...\n" + b.String() + "ok\n[exit:0 | 12ms]"

	clean, refs := Extract(text)
	if len(refs) != 1 {
		t.Fatalf("want 1 ref, got %d", len(refs))
	}
	got := refs[0]
	if got.V != 1 || got.Type != "task" || got.ID != "abc" || got.Intent != "created" {
		t.Fatalf("ref mismatch: %+v", got)
	}
	if got.Preview == nil || got.Preview.Title != "Fix login" || got.Preview.Status != "draft" {
		t.Fatalf("preview mismatch: %+v", got.Preview)
	}
	if strings.Contains(clean, marker) {
		t.Fatalf("sentinel not stripped: %q", clean)
	}
	if !strings.Contains(clean, "creating task...") || !strings.Contains(clean, "[exit:0 | 12ms]") {
		t.Fatalf("real output lost: %q", clean)
	}
}

func TestEmitSkipsIncomplete(t *testing.T) {
	var b strings.Builder
	if err := Emit(&b, Reference{Type: "task"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if b.Len() != 0 {
		t.Fatalf("expected no output for id-less ref, got %q", b.String())
	}
}

func TestExtractNoMarker(t *testing.T) {
	clean, refs := Extract("plain output\nno sentinel here")
	if refs != nil {
		t.Fatalf("expected no refs, got %+v", refs)
	}
	if clean != "plain output\nno sentinel here" {
		t.Fatalf("text mutated: %q", clean)
	}
}

func TestExtractIgnoresMidLineMention(t *testing.T) {
	// A line that merely mentions the marker mid-text is not a sentinel.
	text := "the protocol uses " + marker + "{...} as a prefix"
	clean, refs := Extract(text)
	if len(refs) != 0 {
		t.Fatalf("expected no refs, got %+v", refs)
	}
	if clean != text {
		t.Fatalf("text mutated: %q", clean)
	}
}

func TestExtractDropsMalformedSentinel(t *testing.T) {
	// A sentinel whose payload is truncated/corrupt (e.g. clipped by tail
	// truncation) must be dropped, never surfaced to the user as garbage.
	cases := []string{
		marker + `{"v":1,"type":"ta`,            // truncated JSON
		marker + `{"v":1,"type":"task"}`,        // valid JSON but missing id
		marker + `not json at all`,              // not JSON
		"  " + marker + `{"v":1,"type":"task"}`, // leading whitespace, missing id
	}
	for _, bad := range cases {
		clean, refs := Extract("real output\n" + bad + "\nmore output")
		if len(refs) != 0 {
			t.Errorf("%q: expected no refs, got %+v", bad, refs)
		}
		if strings.Contains(clean, marker) {
			t.Errorf("%q: malformed sentinel leaked into clean text: %q", bad, clean)
		}
		if clean != "real output\nmore output" {
			t.Errorf("%q: unexpected clean text: %q", bad, clean)
		}
	}
}

func TestExtractMultiple(t *testing.T) {
	var b strings.Builder
	_ = Emit(&b, Reference{Type: "task", ID: "t1"})
	_ = Emit(&b, Reference{Type: "goal", ID: "g1"})
	_, refs := Extract("head\n" + b.String() + "tail")
	if len(refs) != 2 || refs[0].ID != "t1" || refs[1].ID != "g1" {
		t.Fatalf("want [t1 g1], got %+v", refs)
	}
}
