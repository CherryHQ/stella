package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	pluginmgr "github.com/vaayne/anna/internal/plugin"
)

func pluginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "plugin",
		Usage: "Manage plugins",
		Subcommands: []*ucli.Command{
			pluginListCommand(),
			pluginAddCommand(),
			pluginRemoveCommand(),
		},
		Action: func(c *ucli.Context) error {
			return pluginListAction()
		},
	}
}

func pluginListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List all configured plugins",
		Action: func(c *ucli.Context) error {
			return pluginListAction()
		},
	}
}

func pluginListAction() error {
	store, err := openStore()
	if err != nil {
		return err
	}

	plugins, err := loadPlugins(store)
	if err != nil {
		return err
	}

	if len(plugins) == 0 {
		fmt.Println("No plugins configured.")
		return nil
	}

	fmt.Printf("%-20s %s\n", "NAME", "PATH")
	for _, p := range plugins {
		name := pluginName(p.Path)
		fmt.Printf("%-20s %s\n", name, p.Path)
	}
	return nil
}

func pluginAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Add a JS plugin",
		ArgsUsage: "<path>",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:  "config",
				Usage: "Plugin config key=value (repeatable)",
			},
		},
		Action: func(c *ucli.Context) error {
			path := c.Args().First()
			if path == "" {
				return fmt.Errorf("usage: anna plugin add <path>")
			}

			resolved := pluginmgr.ExpandPath(path)
			absPath, err := filepath.Abs(resolved)
			if err != nil {
				return fmt.Errorf("resolve absolute path: %w", err)
			}
			if _, err := os.Stat(absPath); err != nil {
				return fmt.Errorf("path %q does not exist", path)
			}

			if !strings.HasSuffix(absPath, ".js") {
				return fmt.Errorf("only .js plugins are supported")
			}

			pluginCfg := parsePluginConfig(c.StringSlice("config"))

			store, err := openStore()
			if err != nil {
				return err
			}

			plugins, err := loadPlugins(store)
			if err != nil {
				return err
			}

			// Deduplicate by path.
			for _, p := range plugins {
				if p.Path == absPath {
					return fmt.Errorf("plugin %q is already configured", absPath)
				}
			}

			plugins = append(plugins, config.PluginConfig{
				Path:   absPath,
				Config: pluginCfg,
			})

			if err := savePlugins(store, plugins); err != nil {
				return err
			}

			name := pluginName(absPath)
			fmt.Printf("Plugin %q added.\n", name)
			return nil
		},
	}
}

func pluginRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Remove a plugin by name or path",
		ArgsUsage: "<name|path>",
		Action: func(c *ucli.Context) error {
			target := c.Args().First()
			if target == "" {
				return fmt.Errorf("usage: anna plugin remove <name|path>")
			}

			store, err := openStore()
			if err != nil {
				return err
			}

			plugins, err := loadPlugins(store)
			if err != nil {
				return err
			}

			var remaining []config.PluginConfig
			found := false
			for _, p := range plugins {
				if p.Path == target || pluginName(p.Path) == target {
					found = true
					continue
				}
				remaining = append(remaining, p)
			}

			if !found {
				return fmt.Errorf("plugin %q not found", target)
			}

			if err := savePlugins(store, remaining); err != nil {
				return err
			}

			fmt.Printf("Plugin %q removed.\n", target)
			return nil
		},
	}
}

// loadPlugins reads the plugins list from the settings table.
func loadPlugins(store config.Store) ([]config.PluginConfig, error) {
	val, err := store.GetSetting(context.Background(), "plugins")
	if err != nil || val == "" {
		return nil, nil
	}
	var plugins []config.PluginConfig
	if err := json.Unmarshal([]byte(val), &plugins); err != nil {
		return nil, fmt.Errorf("parse plugins setting: %w", err)
	}
	return plugins, nil
}

// savePlugins writes the plugins list to the settings table.
func savePlugins(store config.Store, plugins []config.PluginConfig) error {
	data, err := json.Marshal(plugins)
	if err != nil {
		return fmt.Errorf("marshal plugins: %w", err)
	}
	return store.SetSetting(context.Background(), "plugins", string(data))
}

// parsePluginConfig parses key=value strings into a map.
func parsePluginConfig(pairs []string) map[string]any {
	if len(pairs) == 0 {
		return nil
	}
	m := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// pluginName returns a human-friendly name from a plugin path.
func pluginName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
