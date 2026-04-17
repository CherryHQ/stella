package main

import (
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/tools"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func toolsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "tools",
		Usage: "Manage downloadable CLI tools",
		Subcommands: []*ucli.Command{
			toolsListCommand(),
			toolsInstallCommand(),
			toolsUpgradeCommand(),
		},
		Action: func(c *ucli.Context) error {
			return toolsListAction()
		},
	}
}

func toolsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "Show all downloadable tools and their status",
		Action: func(c *ucli.Context) error {
			return toolsListAction()
		},
	}
}

func toolsListAction() error {
	specs, err := pluginhost.DefaultCatalogBinarySpecs()
	if err != nil {
		return fmt.Errorf("load tool catalog: %w", err)
	}
	binDir := tools.BinDir(config.AnnaHome())
	statuses := tools.StatusFromSpecs(specs, binDir)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSTATUS")
	for _, s := range statuses {
		status := "not installed"
		if s.Installed && s.Current {
			status = "installed"
		} else if s.Installed {
			status = "outdated"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Version, status)
	}
	return w.Flush()
}

func toolsInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "install",
		Usage:     "Download and install tools (all if no args)",
		ArgsUsage: "[name...]",
		Action: func(c *ucli.Context) error {
			allSpecs, err := pluginhost.DefaultCatalogBinarySpecs()
			if err != nil {
				return fmt.Errorf("load tool catalog: %w", err)
			}
			logger := slog.Default()

			if c.NArg() == 0 {
				for _, spec := range tools.DeduplicateByName(allSpecs, logger) {
					if err := tools.InstallBinarySpec(c.Context, spec, config.AnnaHome(), logger); err != nil {
						return fmt.Errorf("install %s: %w", spec.Name, err)
					}
					fmt.Printf("Installed %s\n", spec.Name)
				}
				return nil
			}

			index := specsByName(allSpecs)
			for _, name := range c.Args().Slice() {
				spec, ok := index[name]
				if !ok {
					return fmt.Errorf("unknown tool: %s", name)
				}
				if err := tools.InstallBinarySpec(c.Context, spec, config.AnnaHome(), logger); err != nil {
					return fmt.Errorf("install %s: %w", name, err)
				}
				fmt.Printf("Installed %s\n", name)
			}
			return nil
		},
	}
}

func toolsUpgradeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "upgrade",
		Usage:     "Upgrade tools to latest GitHub release (all if no args)",
		ArgsUsage: "[name...]",
		Action: func(c *ucli.Context) error {
			allSpecs, err := pluginhost.DefaultCatalogBinarySpecs()
			if err != nil {
				return fmt.Errorf("load tool catalog: %w", err)
			}
			logger := slog.Default()

			targets := tools.DeduplicateByName(allSpecs, logger)
			if c.NArg() > 0 {
				index := specsByName(allSpecs)
				targets = nil
				for _, name := range c.Args().Slice() {
					spec, ok := index[name]
					if !ok {
						return fmt.Errorf("unknown tool: %s", name)
					}
					targets = append(targets, spec)
				}
			}

			for _, spec := range targets {
				if err := tools.UpgradeBinarySpec(c.Context, spec, config.AnnaHome(), logger); err != nil {
					return fmt.Errorf("upgrade %s: %w", spec.Name, err)
				}
				fmt.Printf("Upgraded %s\n", spec.Name)
			}
			return nil
		},
	}
}

func specsByName(specs []pkgplugins.BinarySpec) map[string]pkgplugins.BinarySpec {
	m := make(map[string]pkgplugins.BinarySpec, len(specs))
	for _, s := range specs {
		if _, exists := m[s.Name]; !exists {
			m[s.Name] = s
		}
	}
	return m
}
