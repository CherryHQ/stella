package cli

import (
	"encoding/json"
	"fmt"
	"io"

	ucli "github.com/urfave/cli/v2"
)

// LineWriter is a fmt-style writer bound to a command's stdout that defers
// error handling: it records the first write error and short-circuits the
// rest, so human-output helpers check once at the end (via Err) instead of
// after every line.
type LineWriter struct {
	w   io.Writer
	err error
}

func Stdout(c *ucli.Context) *LineWriter { return &LineWriter{w: c.App.Writer} }

func Stderr(c *ucli.Context) *LineWriter { return &LineWriter{w: c.App.ErrWriter} }

func (l *LineWriter) Printf(format string, a ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintf(l.w, format, a...)
}

func (l *LineWriter) Println(a ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintln(l.w, a...)
}

func (l *LineWriter) Err() error { return l.err }

func IsJSON(c *ucli.Context) bool { return c.Bool("json") }

func JSONFlag() ucli.Flag {
	return &ucli.BoolFlag{Name: "json", Usage: "Output as JSON"}
}

func PrintJSON(c *ucli.Context, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if _, err := fmt.Fprintln(c.App.Writer, string(out)); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

type deletedResult struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

func PrintDeleted(c *ucli.Context, id string) error {
	return PrintJSON(c, deletedResult{ID: id, Deleted: true})
}
