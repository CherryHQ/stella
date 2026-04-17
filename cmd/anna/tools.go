package main

import (
	"fmt"
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
	binDir := tools.BinDir(config.AnnaHome())
	specs := pluginhost.DefaultCatalogBinarySpecs()
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
			binDir := tools.BinDir(config.AnnaHome())
			platform := tools.Platform()
			allSpecs := pluginhost.DefaultCatalogBinarySpecs()

			if c.NArg() == 0 {
				for _, spec := range deduplicateByName(allSpecs) {
					tool := specToToolForCLI(spec)
					if err := tools.Download(c.Context, &tool, binDir, platform); err != nil {
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
				tool := specToToolForCLI(spec)
				if err := tools.Download(c.Context, &tool, binDir, platform); err != nil {
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
			binDir := tools.BinDir(config.AnnaHome())
			platform := tools.Platform()
			allSpecs := pluginhost.DefaultCatalogBinarySpecs()

			targets := deduplicateByName(allSpecs)
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
				tool := specToToolForCLI(spec)
				if err := tools.DownloadLatest(c.Context, &tool, binDir, platform); err != nil {
					return fmt.Errorf("upgrade %s: %w", spec.Name, err)
				}
				fmt.Printf("Upgraded %s\n", spec.Name)
			}
			return nil
		},
	}
}

func deduplicateByName(specs []pkgplugins.BinarySpec) []pkgplugins.BinarySpec {
	seen := map[string]struct{}{}
	out := make([]pkgplugins.BinarySpec, 0, len(specs))
	for _, s := range specs {
		if _, ok := seen[s.Name]; ok {
			continue
		}
		seen[s.Name] = struct{}{}
		out = append(out, s)
	}
	return out
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

func specToToolForCLI(spec pkgplugins.BinarySpec) tools.Tool {
	templates := make(map[string]tools.AssetTemplate, len(spec.AssetTemplates))
	for k, v := range spec.AssetTemplates {
		templates[k] = tools.AssetTemplate{File: v.File, RawBinary: v.RawBinary}
	}
	return tools.Tool{
		Name:           spec.Name,
		Repo:           spec.Repo,
		Version:        spec.Version,
		AssetTemplates: templates,
	}
}
