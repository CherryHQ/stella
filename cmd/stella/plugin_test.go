package main

import (
	"testing"
)

func TestYesNo(t *testing.T) {
	if got := yesNo(true); got != "yes" {
		t.Errorf("yesNo(true) = %q, want yes", got)
	}
	if got := yesNo(false); got != "no" {
		t.Errorf("yesNo(false) = %q, want no", got)
	}
}

func TestTruncateJSON(t *testing.T) {
	tests := []struct {
		name   string
		m      map[string]any
		maxLen int
		want   string
	}{
		{"empty", map[string]any{}, 40, "{}"},
		{"nil", nil, 40, "{}"},
		{"short", map[string]any{"k": "v"}, 40, `{"k":"v"}`},
		{"long", map[string]any{"very_long_key": "very_long_value_here"}, 20, `{"very_long_key":...`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateJSON(tt.m, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseConfigValue(t *testing.T) {
	tests := []struct {
		input string
		want  any
	}{
		{"42", float64(42)},
		{"true", true},
		{"false", false},
		{`"hello"`, "hello"},
		{"hello", "hello"},
		{`{"k":"v"}`, map[string]any{"k": "v"}},
	}
	for _, tt := range tests {
		got := parseConfigValue(tt.input)
		switch v := tt.want.(type) {
		case float64:
			if g, ok := got.(float64); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case bool:
			if g, ok := got.(bool); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case string:
			if g, ok := got.(string); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case map[string]any:
			if g, ok := got.(map[string]any); !ok || g["k"] != v["k"] {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		}
	}
}
