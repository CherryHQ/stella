package main

import (
	"encoding/json"
	"fmt"

	ucli "github.com/urfave/cli/v2"
)

// isJSON reports whether the command was asked for machine-readable output.
func isJSON(c *ucli.Context) bool { return c.Bool("json") }

// jsonFlag returns the standard --json output flag.
func jsonFlag() ucli.Flag {
	return &ucli.BoolFlag{Name: "json", Usage: "Output as JSON"}
}

// printJSON writes v as indented JSON to the command's stdout. Routing through
// c.App.Writer (rather than os.Stdout) keeps stdout/stderr separation testable.
func printJSON(c *ucli.Context, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if _, err := fmt.Fprintln(c.App.Writer, string(out)); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// deletedResult is the stable JSON shape for delete/remove commands, which have
// no HTTP response body (204) but still need a scriptable confirmation.
type deletedResult struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// printDeleted emits the standard deleted-resource object under --json.
func printDeleted(c *ucli.Context, id string) error {
	return printJSON(c, deletedResult{ID: id, Deleted: true})
}
