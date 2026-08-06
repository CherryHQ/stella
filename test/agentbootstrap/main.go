// Command agentbootstrap creates disposable credentials for an isolated local
// Stella development instance. It is intentionally outside production code:
// all state is created through the running server's public HTTP API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("agent-test:bootstrap", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baseURL := flags.String("base-url", "http://127.0.0.1:25678", "URL of the running Stella server")
	home := flags.String("home", "", "isolated STELLA_HOME that owns the credential artifact")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		return 2
	}
	if *home == "" {
		fmt.Fprintln(os.Stderr, "--home is required")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	path, reused, err := bootstrap(ctx, bootstrapConfig{BaseURL: *baseURL, Home: *home})
	if err != nil {
		// bootstrap errors deliberately exclude response bodies, headers, and
		// credentials so this command is safe to run in a shared terminal.
		fmt.Fprintln(os.Stderr, "agent test bootstrap:", err)
		return 1
	}
	if reused {
		fmt.Println("agent test credentials already exist:", path)
		return 0
	}
	fmt.Println("agent test credentials written:", path)
	return 0
}
