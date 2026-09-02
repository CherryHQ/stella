package cli

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"TRACE", slog.Level(-8)},
		{"trace", slog.Level(-8)},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		// Empty and unrecognized values default to INFO.
		{"", slog.LevelInfo},
		{"bogus", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := ParseLogLevel(tc.raw); got != tc.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestMinLevelHandlerDropsBelowMin(t *testing.T) {
	var buf bytes.Buffer
	// Inner handler accepts everything down to DEBUG; the wrapper caps at WARN.
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	log := slog.New(NewMinLevelHandler(slog.LevelWarn, inner)).With("component", "river")

	log.Debug("dropped debug")
	log.Info("dropped info")
	log.Warn("kept warn")
	log.Error("kept error")

	out := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("dropped")) {
		t.Errorf("records below min leaked through: %q", out)
	}
	for _, want := range []string{"kept warn", "kept error", "component=river"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}
