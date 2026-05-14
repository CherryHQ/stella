package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// parseLogLevel maps LOG_LEVEL env var to slog.Level.
// Supported values: TRACE, DEBUG, INFO, WARN, ERROR (case-insensitive).
// Defaults to DEBUG if unset or unrecognized.
func parseLogLevel() slog.Level {
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "TRACE":
		return slog.Level(-8) // below Debug, enables memory trace detail
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

func main() {
	loadDotEnv()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(),
	})))

	app := newApp()

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
