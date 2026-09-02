package main

import (
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func miseCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "mise",
		Usage:    "Manage the builtin mise tool tree",
		Category: "System",
		Subcommands: []*ucli.Command{
			miseReconcileBuiltinsCommand(),
		},
	}
}

// miseReconcileBuiltinsCommand installs the builtin manifest tools into
// $STELLA_HOME/.mise-tools using the exact reconcile path the daemon runs. The
// sandbox image build calls it (STELLA_HOME=/opt/stella) so the image's system
// tree carries the same tool identifiers and versions as the host — making the
// per-user relative seed symlinks resolve and backend switching transparent.
func miseReconcileBuiltinsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "reconcile-builtins",
		Usage: "Install builtin manifest tools into $STELLA_HOME/.mise-tools (used to bake the sandbox image)",
		Action: func(c *ucli.Context) error {
			m, err := manifest.LoadBuiltin()
			if err != nil {
				return fmt.Errorf("load builtin manifest: %w", err)
			}
			stellaHome := config.StellaHome()
			result := manifest.Reconcile(c.Context, m, stellaHome)

			var failures []string
			for _, pr := range result.Plugins {
				if pr.Err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", pr.PluginID, pr.Err))
				}
				for _, b := range pr.Binaries {
					if b.Err != nil {
						failures = append(failures, fmt.Sprintf("%s/%s: %v", pr.PluginID, b.Name, b.Err))
					}
				}
			}
			if len(failures) > 0 {
				return fmt.Errorf("reconcile builtins failed:\n  %s", strings.Join(failures, "\n  "))
			}

			fmt.Printf("reconciled %d builtin plugins into %s\n", result.EnabledCount, stellaHome)
			return nil
		},
	}
}
