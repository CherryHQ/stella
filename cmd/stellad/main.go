package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/observability"
)

func main() {
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
