package cli

import (
	"context"
	"log/slog"
	"strings"
)

// ParseLogLevel maps a LOG_LEVEL value to slog.Level.
// Supported values: TRACE, DEBUG, INFO, WARN, ERROR (case-insensitive).
// Defaults to INFO if empty or unrecognized. It is a pure function of its
// argument: logging is configured before the server config is parsed, so the
// caller (main) passes the raw environment value.
func ParseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(raw) {
	case "TRACE":
		return slog.Level(-8)
	case "DEBUG":
		return slog.LevelDebug
	case "INFO", "":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewMinLevelHandler wraps next and drops records below min, regardless of
// what next would accept. It exists so chatty subsystem loggers (e.g. the
// River job queue, which heartbeats at DEBUG/INFO every few seconds) can be
// capped independently of the process-wide LOG_LEVEL.
func NewMinLevelHandler(min slog.Level, next slog.Handler) slog.Handler {
	return minLevelHandler{min: min, next: next}
}

type minLevelHandler struct {
	min  slog.Level
	next slog.Handler
}

func (h minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.next.Enabled(ctx, level)
}

func (h minLevelHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, record)
}

func (h minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevelHandler{min: h.min, next: h.next.WithAttrs(attrs)}
}

func (h minLevelHandler) WithGroup(name string) slog.Handler {
	return minLevelHandler{min: h.min, next: h.next.WithGroup(name)}
}
