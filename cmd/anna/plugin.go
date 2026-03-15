package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	pluginmgr "github.com/vaayne/anna/internal/plugin"
	"gopkg.in/yaml.v3"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(cfg.Plugins) == 0 {
		fmt.Println("No plugins configured.")
		return nil
	}

	fmt.Printf("%-20s %s\n", "NAME", "PATH")
	for _, p := range cfg.Plugins {
		name := pluginName(p.Path)
		fmt.Printf("%-20s %s\n", name, p.Path)
	}
	return nil
}

func pluginAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Add a JS plugin to config.yaml",
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

			if err := addPluginToConfig(absPath, pluginCfg); err != nil {
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
		Usage:     "Remove a plugin from config.yaml by name or path",
		ArgsUsage: "<name|path>",
		Action: func(c *ucli.Context) error {
			target := c.Args().First()
			if target == "" {
				return fmt.Errorf("usage: anna plugin remove <name|path>")
			}

			if err := removePluginFromConfig(target); err != nil {
				return err
			}

			fmt.Printf("Plugin %q removed.\n", target)
			return nil
		},
	}
}

// addPluginToConfig reads config.yaml, appends the plugin (deduplicating by
// path), and writes it back preserving other fields.
func addPluginToConfig(path string, pluginCfg map[string]any) error {
	raw, err := readConfigRaw()
	if err != nil {
		return err
	}

	plugins := rawPlugins(raw)

	// Deduplicate by path.
	for _, p := range plugins {
		if pm, ok := p.(map[string]any); ok {
			if pm["path"] == path {
				return fmt.Errorf("plugin %q is already configured", path)
			}
		}
	}

	entry := map[string]any{"path": path}
	if len(pluginCfg) > 0 {
		entry["config"] = pluginCfg
	}
	plugins = append(plugins, entry)
	raw["plugins"] = plugins

	return writeConfigRaw(raw)
}

// removePluginFromConfig removes a plugin by matching name or path.
func removePluginFromConfig(target string) error {
	raw, err := readConfigRaw()
	if err != nil {
		return err
	}

	plugins := rawPlugins(raw)
	var remaining []any
	found := false

	for _, p := range plugins {
		pm, ok := p.(map[string]any)
		if !ok {
			remaining = append(remaining, p)
			continue
		}
		pPath, _ := pm["path"].(string)
		if pPath == target || pluginName(pPath) == target {
			found = true
			continue
		}
		remaining = append(remaining, p)
	}

	if !found {
		return fmt.Errorf("plugin %q not found in config", target)
	}

	if len(remaining) == 0 {
		delete(raw, "plugins")
	} else {
		raw["plugins"] = remaining
	}

	return writeConfigRaw(raw)
}

// readConfigRaw reads config.yaml as a generic map to preserve all fields.
func readConfigRaw() (map[string]any, error) {
	data, err := os.ReadFile(config.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw, nil
}

// writeConfigRaw marshals the raw map back to config.yaml.
func writeConfigRaw(raw map[string]any) error {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(config.Path(), data, 0o644)
}

// rawPlugins extracts the plugins slice from a raw config map.
func rawPlugins(raw map[string]any) []any {
	v, ok := raw["plugins"]
	if !ok {
		return nil
	}
	plugins, ok := v.([]any)
	if !ok {
		return nil
	}
	return plugins
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
