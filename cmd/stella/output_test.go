package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	ucli "github.com/urfave/cli/v2"
)

// runWithAction builds a single-command app wired to capture stdout/stderr and
// runs it with the given args, returning the captured streams and run error.
func runWithAction(args []string, flags []ucli.Flag, action ucli.ActionFunc) (stdout, stderr *bytes.Buffer, err error) {
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	app := &ucli.App{
		Name:      "stella",
		Writer:    stdout,
		ErrWriter: stderr,
		Commands: []*ucli.Command{
			{Name: "probe", Flags: flags, Action: action},
		},
	}
	err = app.Run(append([]string{"stella", "probe"}, args...))
	return stdout, stderr, err
}

func TestPrintJSONWritesValidJSONToStdout(t *testing.T) {
	stdout, stderr, err := runWithAction(nil, nil, func(c *ucli.Context) error {
		return printJSON(c, map[string]any{"id": "abc", "n": 1})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr not empty: %q", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if got["id"] != "abc" {
		t.Fatalf("id = %v, want abc", got["id"])
	}
}

func TestPrintDeletedShape(t *testing.T) {
	stdout, _, err := runWithAction(nil, nil, func(c *ucli.Context) error {
		return printDeleted(c, "task-123")
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var got deletedResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.ID != "task-123" || !got.Deleted {
		t.Fatalf("got %+v, want {task-123 true}", got)
	}
}

func TestIsJSONSelectedByFlag(t *testing.T) {
	stdout, _, err := runWithAction([]string{"--json"}, []ucli.Flag{jsonFlag()}, func(c *ucli.Context) error {
		if !isJSON(c) {
			t.Error("isJSON = false with --json set")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = stdout

	_, _, err = runWithAction(nil, []ucli.Flag{jsonFlag()}, func(c *ucli.Context) error {
		if isJSON(c) {
			t.Error("isJSON = true without --json")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

// Mirrors main(): a command Action error must propagate out of app.Run (which
// main routes to stderr + exit 1) and must not leak anything to stdout.
func TestCommandErrorKeepsStdoutClean(t *testing.T) {
	stdout, _, err := runWithAction(nil, nil, func(c *ucli.Context) error {
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error to propagate from app.Run")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty on error, got %q", stdout.String())
	}
}
