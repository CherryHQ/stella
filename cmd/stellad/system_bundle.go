package main

import (
	"fmt"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/resources"
	"github.com/CherryHQ/stella/resources/binaries"
)

func systemBundleCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "system-bundle",
		Usage:    "Inspect and install the builtin skill bundle",
		Category: "System",
		Subcommands: []*ucli.Command{
			systemBundleRevisionCommand(),
			systemBundleInstallCommand(),
			systemBundleVerifyCommand(),
		},
	}
}

func systemBundleRevisionCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "revision",
		Usage: "Print the builtin skill bundle revision",
		Action: func(c *ucli.Context) error {
			registry, err := resources.Default()
			if err != nil {
				return fmt.Errorf("load builtin skill bundle: %w", err)
			}
			if _, err := fmt.Fprintln(c.App.Writer, registry.BundleRevision()); err != nil {
				return fmt.Errorf("write builtin skill bundle revision: %w", err)
			}
			return nil
		},
	}
}

func systemBundleInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "install",
		Usage: "Install the verified builtin skill bundle into $STELLA_HOME",
		Action: func(c *ucli.Context) error {
			if err := binaries.EnsureTools(config.StellaHome()); err != nil {
				return fmt.Errorf("install embedded runtimes: %w", err)
			}
			if err := binaries.VerifyTools(config.StellaHome()); err != nil {
				return err
			}
			registry, err := resources.Default()
			if err != nil {
				return fmt.Errorf("load builtin skill bundle: %w", err)
			}
			bundlePath, err := registry.InstallBuiltinBundle(config.StellaHome())
			if err != nil {
				return fmt.Errorf("install builtin skill bundle: %w", err)
			}
			if _, err := fmt.Fprintf(c.App.Writer, "installed builtin skill bundle %s at %s\n", registry.BundleRevision(), bundlePath); err != nil {
				return fmt.Errorf("write builtin skill bundle install result: %w", err)
			}
			return nil
		},
	}
}

func systemBundleVerifyCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "verify",
		Usage: "Verify the builtin skill bundle installed in $STELLA_HOME",
		Action: func(c *ucli.Context) error {
			registry, err := resources.Default()
			if err != nil {
				return fmt.Errorf("load builtin skill bundle: %w", err)
			}
			if err := registry.VerifyBuiltinBundle(config.StellaHome()); err != nil {
				return fmt.Errorf("verify builtin skill bundle: %w", err)
			}
			if _, err := fmt.Fprintf(c.App.Writer, "verified builtin skill bundle %s\n", registry.BundleRevision()); err != nil {
				return fmt.Errorf("write builtin skill bundle verification result: %w", err)
			}
			return nil
		},
	}
}
