package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/observability"
)

func main() {
	if filepath.Base(os.Args[0]) == "stella-fs" {
		if err := fsops.RunHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cli.LoadDotEnv()

	// The trace-context wrapper stamps trace_id/span_id onto records logged
	// with a span-carrying context, so stderr lines correlate with exported
	// traces. It is inert (a map lookup) when tracing is off.
	slog.SetDefault(slog.New(observability.NewTraceContextHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: cli.ParseLogLevel(os.Getenv("LOG_LEVEL")),
		}))))

	app := newApp()

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
