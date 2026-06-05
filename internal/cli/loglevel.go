package cli

import (
	"log/slog"
	"os"
	"strings"
)

// ParseLogLevel maps the LOG_LEVEL env var to slog.Level.
// Supported values: TRACE, DEBUG, INFO, WARN, ERROR (case-insensitive).
// Defaults to DEBUG if unset or unrecognized.
func ParseLogLevel() slog.Level {
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
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
