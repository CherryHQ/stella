package main

import (
	"encoding/json"
	"fmt"
	"io"

	ucli "github.com/urfave/cli/v2"
)

// lineWriter is a fmt-style writer bound to a command's stdout that defers
// error handling: it records the first write error and short-circuits the
// rest, so human-output helpers check once at the end (via Err) instead of
// after every line.
type lineWriter struct {
	w   io.Writer
	err error
}

// stdout returns a lineWriter over the command's stdout sink.
func stdout(c *ucli.Context) *lineWriter { return &lineWriter{w: c.App.Writer} }

func (l *lineWriter) printf(format string, a ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintf(l.w, format, a...)
}

func (l *lineWriter) println(a ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintln(l.w, a...)
}

// Err returns the first write error encountered, if any.
func (l *lineWriter) Err() error { return l.err }

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
