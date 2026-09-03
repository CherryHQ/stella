package testbed

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func configuredPort() (int, error) {
	raw := os.Getenv("STELLA_TESTBED_PORT")
	if raw == "" {
		return 25777, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("STELLA_TESTBED_PORT=%q is not a port between 1 and 65535", raw)
	}
	return port, nil
}

func RunCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: testbed start|stop")
		return 2
	}
	switch args[0] {
	case "start":
		configured, err := configuredPort()
		if err != nil {
			fmt.Fprintln(os.Stderr, "testbed start:", err)
			return 2
		}
		flags := flag.NewFlagSet("testbed start", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		port := flags.Int("port", configured, "Stella HTTP port (default $STELLA_TESTBED_PORT, else 25777)")
		fakeModel := flags.Bool("fake-model", false, "start an embedded fake Anthropic provider")
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
		instance, err := Start(ctx, Options{RepoRoot: cwd, Port: *port, FakeModel: *fakeModel, Bootstrap: true, Managed: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, "testbed start:", err)
			return 1
		}
		fmt.Println("Stella testbed:", instance.BaseURL())
		fmt.Println("Credentials:", instance.credentialsPath)
		fmt.Println("Stop and clean up: mise run testbed:stop")
		<-ctx.Done()
		_ = instance.Stop()
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
