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

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
