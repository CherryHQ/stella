package lcm

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3}, // 11 chars → (11+3)/4 = 3
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestCompactionMode(t *testing.T) {
	// Pin iota values to prevent accidental reordering.
	if CompactionIncremental != 0 {
		t.Errorf("CompactionIncremental = %d, want 0", CompactionIncremental)
	}
	if CompactionFull != 1 {
		t.Errorf("CompactionFull = %d, want 1", CompactionFull)
	}

	// String() method
	if s := CompactionIncremental.String(); s != "incremental" {
		t.Errorf("CompactionIncremental.String() = %q, want %q", s, "incremental")
	}
	if s := CompactionFull.String(); s != "full" {
		t.Errorf("CompactionFull.String() = %q, want %q", s, "full")
	}
	if s := CompactionMode(99).String(); s != "unknown" {
		t.Errorf("CompactionMode(99).String() = %q, want %q", s, "unknown")
	}
}

func TestConstants(t *testing.T) {
	// Verify constants are distinct and non-empty
	kinds := []string{KindLeaf, KindCondensed}
	roles := []string{RoleUser, RoleAssistant, RoleTool}
	parts := []string{PartTypeText, PartTypeReasoning, PartTypeTool}
	items := []string{ItemTypeMessage, ItemTypeSummary}

	for _, group := range [][]string{kinds, roles, parts, items} {
		seen := make(map[string]bool)
		for _, v := range group {
			if v == "" {
				t.Error("constant should not be empty")
			}
			if seen[v] {
				t.Errorf("duplicate constant: %q", v)
			}
			seen[v] = true
		}
	}
}
