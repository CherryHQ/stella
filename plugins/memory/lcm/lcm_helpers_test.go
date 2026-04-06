package lcm

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
)

func TestToInt(t *testing.T) {
	tests := []struct {
		input any
		want  int
		ok    bool
	}{
		{float64(5.0), 5, true},
		{int(3), 3, true},
		{int64(7), 7, true},
		{"string", 0, false},
		{nil, 0, false},
	}
	for _, tc := range tests {
		got, ok := toInt(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Errorf("toInt(%v) = (%d, %v), want (%d, %v)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		input string
		empty bool
	}{
		{"2024-01-15 10:30:00", false},
		{"2024-01-15T10:30:00Z", false},
		{"2024-01-15T10:30:00+00:00", false},
		{"invalid", true},
		{"", true},
	}
	for _, tc := range tests {
		got := parseTime(tc.input)
		if tc.empty && !got.IsZero() {
			t.Errorf("parseTime(%q): expected zero time, got %v", tc.input, got)
		}
		if !tc.empty && got.IsZero() {
			t.Errorf("parseTime(%q): expected non-zero time", tc.input)
		}
	}
}

func TestParseNullTime(t *testing.T) {
	// Invalid null string.
	ns := sql.NullString{Valid: false, String: ""}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for invalid null string")
	}

	// Valid but unparseable.
	ns = sql.NullString{Valid: true, String: "notadate"}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for unparseable date")
	}

	// Valid and parseable.
	ns = sql.NullString{Valid: true, String: "2024-01-15 10:30:00"}
	got := parseNullTime(ns)
	if got == nil {
		t.Error("expected non-nil time for valid date")
	}
}

func TestGenerateSummaryID(t *testing.T) {
	id1 := generateSummaryID()
	id2 := generateSummaryID()

	if !strings.HasPrefix(id1, "sum_") {
		t.Errorf("expected 'sum_' prefix, got %q", id1)
	}
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	// "sum_" + 16 hex chars = 20 chars.
	if len(id1) != 20 {
		t.Errorf("expected 20 char ID, got %d: %q", len(id1), id1)
	}
}

func TestParseNullTime_EmptyString(t *testing.T) {
	ns := sql.NullString{Valid: true, String: ""}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for empty valid string")
	}
}

func TestFormatSummaryXML_Leaf(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:      "sum-1",
		Kind:    "leaf",
		Depth:   0,
		Content: "summary content here",
	}
	got := FormatSummaryXML(sum, nil)
	if !strings.Contains(got, `id="sum-1"`) {
		t.Errorf("expected id attribute, got %q", got)
	}
	if !strings.Contains(got, "summary content here") {
		t.Errorf("expected content, got %q", got)
	}
	if !strings.Contains(got, `<summary`) && !strings.Contains(got, `</summary>`) {
		t.Errorf("expected XML tags, got %q", got)
	}
}

func TestFormatSummaryXML_Condensed(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:              "sum-2",
		Kind:            kindCondensed,
		Depth:           1,
		Content:         "condensed content",
		DescendantCount: 5,
		EarliestAt:      sql.NullString{Valid: true, String: "2024-01-01 00:00:00"},
		LatestAt:        sql.NullString{Valid: true, String: "2024-01-02 00:00:00"},
	}
	parent := sqlc.CtxSummary{ID: "parent-1"}
	got := FormatSummaryXML(sum, []sqlc.CtxSummary{parent})

	if !strings.Contains(got, `descendant_count="5"`) {
		t.Errorf("expected descendant_count, got %q", got)
	}
	if !strings.Contains(got, `earliest_at=`) {
		t.Errorf("expected earliest_at, got %q", got)
	}
	if !strings.Contains(got, `<summary_ref id="parent-1"`) {
		t.Errorf("expected parent ref, got %q", got)
	}
}

func TestFormatSummaryXML_ContentWithNewline(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:      "sum-3",
		Kind:    "leaf",
		Content: "content with newline\n",
	}
	got := FormatSummaryXML(sum, nil)
	// Content already ends with \n, should not add another.
	if strings.Contains(got, "newline\n\n") {
		t.Errorf("should not double-add newline, got %q", got)
	}
}

func TestTruncateUTF8_NoTruncation(t *testing.T) {
	got := truncateUTF8("hello", 10)
	if got != "hello" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestTruncateUTF8_Truncation(t *testing.T) {
	got := truncateUTF8("hello world", 5)
	if got != "hello..." {
		t.Errorf("expected 'hello...', got %q", got)
	}
}

func TestTruncateUTF8_UTF8(t *testing.T) {
	got := truncateUTF8("日本語テスト", 3)
	if got != "日本語..." {
		t.Errorf("expected '日本語...', got %q", got)
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Error("expected boolToInt(true)=1")
	}
	if boolToInt(false) != 0 {
		t.Error("expected boolToInt(false)=0")
	}
}

// Test that parseTime handles all three layouts.
func TestParseTime_AllLayouts(t *testing.T) {
	cases := []struct {
		input  string
		layout string
	}{
		{"2024-03-15 12:00:00", "SQLite format"},
		{"2024-03-15T12:00:00Z", "ISO8601"},
		{time.Now().UTC().Format(time.RFC3339), "RFC3339"},
	}
	for _, tc := range cases {
		got := parseTime(tc.input)
		if got.IsZero() {
			t.Errorf("parseTime(%q) [%s]: expected non-zero time", tc.input, tc.layout)
		}
	}
}
