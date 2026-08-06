// Command testbed runs a disposable local Stella instance for API and browser
// tests. It is deliberately test-only: production lifecycle is owned by
// stellad, while this command owns every resource it creates.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: testbed start|stop")
		return 2
	}
	switch args[0] {
	case "start":
		flags := flag.NewFlagSet("testbed start", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		port := flags.Int("port", defaultPort, "Stella HTTP port")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *port < 1 || *port > 65535 {
			if err == nil {
				fmt.Fprintln(os.Stderr, "--port must be between 1 and 65535")
			}
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "testbed start: get working directory:", err)
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := start(ctx, config{RepoRoot: cwd, Port: *port}); err != nil {
			fmt.Fprintln(os.Stderr, "testbed start:", err)
			return 1
		}
		return 0
	case "stop":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "testbed stop accepts no arguments")
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "testbed stop: get working directory:", err)
			return 1
		}
		if err := stop(context.Background(), statePath(cwd)); err != nil {
			fmt.Fprintln(os.Stderr, "testbed stop:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: testbed start|stop")
		return 2
	}
}

func binaryPath(repoRoot string) string { return filepath.Join(repoRoot, "dist", "bin", "stellad") }
