//go:build capability

package main

import (
	"path/filepath"
	"sort"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/test/capabilities"
)

// TestCapabilityCommandSurface traverses the real urfave/cli tree. Keeping the
// check in package main avoids exporting production-only construction helpers.
func TestCapabilityCommandSurface(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	manifest, err := capabilities.LoadManifest(filepath.Join(repositoryRoot, "test", "capabilities.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	actual := commandPaths("stellad", newApp().Commands)
	if err := capabilities.ValidateCLICommands(manifest, actual); err != nil {
		t.Fatal(err)
	}
}

func commandPaths(parent string, commands []*ucli.Command) []string {
	var paths []string
	for _, command := range commands {
		path := parent + " " + command.Name
		paths = append(paths, path)
		paths = append(paths, commandPaths(path, command.Subcommands)...)
	}
	sort.Strings(paths)
	return paths
}
