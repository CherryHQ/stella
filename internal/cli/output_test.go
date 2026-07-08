package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	ucli "github.com/urfave/cli/v2"
)

func runWithAction(args []string, flags []ucli.Flag, action ucli.ActionFunc) (stdout, stderr *bytes.Buffer, err error) {
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	app := &ucli.App{
		Name:      "stellad",
		Writer:    stdout,
		ErrWriter: stderr,
		Commands: []*ucli.Command{
			{Name: "probe", Flags: flags, Action: action},
		},
	}
	err = app.Run(append([]string{"stellad", "probe"}, args...))
	return stdout, stderr, err
}

func TestPrintJSONWritesValidJSONToStdout(t *testing.T) {
	stdout, stderr, err := runWithAction(nil, nil, func(c *ucli.Context) error {
		return PrintJSON(c, map[string]any{"id": "abc", "n": 1})
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

func TestIsJSONSelectedByFlag(t *testing.T) {
	stdout, _, err := runWithAction([]string{"--json"}, []ucli.Flag{JSONFlag()}, func(c *ucli.Context) error {
		if !IsJSON(c) {
			t.Error("IsJSON = false with --json set")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = stdout

	_, _, err = runWithAction(nil, []ucli.Flag{JSONFlag()}, func(c *ucli.Context) error {
		if IsJSON(c) {
			t.Error("IsJSON = true without --json")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

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
