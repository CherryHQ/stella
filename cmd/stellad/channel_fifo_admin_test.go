package main

import (
	"bytes"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v2"
)

func TestChannelFIFOAdminCommandShape(t *testing.T) {
	command := channelCommand()
	if command.Name != "channel" {
		t.Fatalf("top-level command name = %q, want channel", command.Name)
	}
	if len(command.Subcommands) != 1 || command.Subcommands[0].Name != "fifo" {
		t.Fatalf("channel subcommands = %+v, want only fifo", command.Subcommands)
	}
	fifo := command.Subcommands[0]
	got := make([]string, 0, len(fifo.Subcommands))
	for _, subcommand := range fifo.Subcommands {
		got = append(got, subcommand.Name)
	}
	if strings.Join(got, ",") != "list,inspect,reject" {
		t.Fatalf("fifo subcommands = %v, want list,inspect,reject", got)
	}
}

func TestChannelFIFOAdminCLIValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "inspect needs one exact ID", args: []string{"channel", "fifo", "inspect"}, want: "exactly one FIFO item ID"},
		{name: "inspect rejects multiple IDs", args: []string{"channel", "fifo", "inspect", "one", "two"}, want: "exactly one FIFO item ID"},
		{name: "reject requires force", args: []string{"channel", "fifo", "reject", "--reason", "operator review", "one"}, want: "--force"},
		{name: "reject requires a non-empty reason", args: []string{"channel", "fifo", "reject", "--reason", "  ", "--force", "one"}, want: "non-empty --reason"},
		{name: "list rejects positional arguments", args: []string{"channel", "fifo", "list", "unexpected"}, want: "does not accept positional arguments"},
		{name: "list rejects zero limit", args: []string{"channel", "fifo", "list", "--limit", "0"}, want: "between 1 and 100"},
		{name: "list rejects oversized limit", args: []string{"channel", "fifo", "list", "--limit", "101"}, want: "between 1 and 100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			app := ucli.NewApp()
			app.Writer = &output
			app.Commands = []*ucli.Command{channelCommand()}
			err := app.Run(append([]string{"stellad"}, tc.args...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestChannelFIFORequiresExternalDatabaseWhenConfigured(t *testing.T) {
	t.Setenv("STELLA_DATABASE_URL", "")
	t.Setenv("STELLA_REQUIRE_EXTERNAL_DB", "true")
	var output bytes.Buffer
	app := ucli.NewApp()
	app.Writer = &output
	app.Commands = []*ucli.Command{channelCommand()}
	err := app.RunContext(t.Context(), []string{"stellad", "channel", "fifo", "list"})
	if err == nil || !strings.Contains(err.Error(), "STELLA_DATABASE_URL is required") {
		t.Fatalf("error = %v, want strict external database guard", err)
	}
}
