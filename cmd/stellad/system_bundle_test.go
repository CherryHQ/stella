package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

var errSystemBundleWriter = errors.New("system bundle output failed")

type failingSystemBundleWriter struct{}

func (failingSystemBundleWriter) Write([]byte) (int, error) { return 0, errSystemBundleWriter }

func TestSystemBundleCommandsUseConfiguredTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	for _, args := range [][]string{
		{"stellad", "system-bundle", "revision"},
		{"stellad", "system-bundle", "install"},
		{"stellad", "system-bundle", "verify"},
	} {
		app := newApp()
		var output bytes.Buffer
		app.Writer = &output
		if err := app.RunContext(context.Background(), args); err != nil {
			t.Fatalf("%s: %v", strings.Join(args[1:], " "), err)
		}
		if strings.TrimSpace(output.String()) == "" {
			t.Fatalf("%s produced no stdout", strings.Join(args[1:], " "))
		}
	}
}

func TestSystemBundleCommandsPropagateWriterErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	for _, args := range [][]string{
		{"stellad", "system-bundle", "revision"},
		{"stellad", "system-bundle", "install"},
		{"stellad", "system-bundle", "verify"},
	} {
		app := newApp()
		app.Writer = failingSystemBundleWriter{}
		err := app.RunContext(context.Background(), args)
		if !errors.Is(err, errSystemBundleWriter) {
			t.Fatalf("%s error = %v, want writer error", strings.Join(args[1:], " "), err)
		}
	}
}
