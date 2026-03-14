package main

import (
	"fmt"
	"log/slog"
	"os"
)

// Main is the exported entry point for custom binaries built with Go plugins.
// Plugin authors import this package and call Main() from their generated main.go.
func Main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	app := newApp()

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	Main()
}
