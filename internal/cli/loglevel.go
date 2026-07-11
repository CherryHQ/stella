package cli

import (
	"log/slog"
	"strings"
)

// ParseLogLevel maps a LOG_LEVEL value to slog.Level.
// Supported values: TRACE, DEBUG, INFO, WARN, ERROR (case-insensitive).
// Defaults to DEBUG if empty or unrecognized. It is a pure function of its
// argument: logging is configured before the server config is parsed, so the
// caller (main) passes the raw environment value.
func ParseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(raw) {
	case "TRACE":
		return slog.Level(-8)
	case "DEBUG", "":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}
