package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/CherryHQ/stella/internal/cli"
)

func main() {
	cli.LoadDotEnv()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: cli.ParseLogLevel(),
	})))

	app := newApp()

	// urfave/cli v2 stops flag parsing at the first positional argument, so
	// `stella goal get <id> --json` would silently ignore --json. Reorder argv
	// so flags work in any position.
	if err := app.Run(cli.HoistFlags(app, os.Args)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
