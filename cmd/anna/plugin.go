package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/tools"
)

func pluginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "plugin",
		Usage: "Manage plugins",
		Subcommands: []*ucli.Command{
			pluginListCommand(),
			pluginEnableCommand(),
			pluginDisableCommand(),
			pluginConfigCommand(),
		},
		Action: pluginListAction,
	}
}

// --- list ---

func pluginListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List all configured plugins",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "kind",
				Usage: "Filter by plugin kind (tool or channel)",
			},
		},
		Action: pluginListAction,
	}
}

func pluginListAction(c *ucli.Context) error {
	store, err := openStore()
	if err != nil {
		return err
	}

	plugins, err := listPlugins(context.Background(), store, c.String("kind"))
	if err != nil {
		return err
	}
	if len(plugins) == 0 {
		fmt.Println("No plugins configured.")
		return nil
	}

	printPlugins(plugins)
	return nil
}

// --- enable ---

func pluginEnableCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "enable",
		Usage:     "Enable a plugin",
		ArgsUsage: "<id>",
		Action: func(c *ucli.Context) error {
			return setPluginEnabled(c, true)
		},
	}
}

// --- disable ---

func pluginDisableCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "disable",
		Usage:     "Disable a plugin",
		ArgsUsage: "<id>",
		Action: func(c *ucli.Context) error {
			return setPluginEnabled(c, false)
		},
	}
}

func setPluginEnabled(c *ucli.Context, enabled bool) error {
	id := c.Args().First()
	if id == "" {
		verb := pluginEnabledVerb(enabled)
		return fmt.Errorf("usage: anna plugin %s <id>", verb)
	}

	store, err := openStore()
	if err != nil {
		return err
	}

	ctx := context.Background()
	if _, err := getPlugin(ctx, store, id); err != nil {
		return err
	}
	if err := store.SetPluginEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("update plugin: %w", err)
	}

	// Auto-download tool binary when a plugin is enabled.
	if enabled {
		tools.EnsurePluginTool(ctx, id, config.AnnaHome(), nil)
	}

	fmt.Printf("Plugin %q %s.\n", id, pluginEnabledState(enabled))
	return nil
}

// --- config ---

func pluginConfigCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "config",
		Usage:     "Show or update plugin configuration",
		ArgsUsage: "<id> [key=val ...]",
		Action:    pluginConfigAction,
	}
}

func pluginConfigAction(c *ucli.Context) error {
	id := c.Args().First()
	if id == "" {
		return fmt.Errorf("usage: anna plugin config <id> [key=val ...]")
	}

	store, err := openStore()
	if err != nil {
		return err
	}

	ctx := context.Background()
	p, err := getPlugin(ctx, store, id)
	if err != nil {
		return err
	}

	// No extra args — show current config.
	pairs := c.Args().Tail()
	if len(pairs) == 0 {
		out, _ := json.MarshalIndent(p.Config, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	// Merge key=value pairs.
	cfg := p.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	for _, kv := range pairs {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid key=value pair: %q", kv)
		}
		cfg[key] = parseConfigValue(val)
	}

	if err := store.SetPluginConfig(ctx, id, cfg); err != nil {
		return fmt.Errorf("update config: %w", err)
	}

	fmt.Printf("Plugin %q config updated.\n", id)
	return nil
}

// --- helpers ---

func listPlugins(ctx context.Context, store config.Store, kind string) ([]config.Plugin, error) {
	if kind == "" {
		return store.ListPlugins(ctx)
	}
	if !isValidPluginKind(kind) {
		return nil, fmt.Errorf("invalid kind %q, must be %q, %q, or %q", kind, config.PluginKindTool, config.PluginKindChannel, config.PluginKindHook)
	}
	return store.ListPluginsByKind(ctx, kind)
}

func getPlugin(ctx context.Context, store config.Store, id string) (config.Plugin, error) {
	plugin, err := store.GetPlugin(ctx, id)
	if err != nil {
		return config.Plugin{}, fmt.Errorf("plugin %q not found", id)
	}
	return plugin, nil
}

func printPlugins(plugins []config.Plugin) {
	fmt.Printf("%-24s %-10s %-8s %s\n", "ID", "KIND", "ENABLED", "CONFIG")
	for _, plugin := range plugins {
		fmt.Printf("%-24s %-10s %-8s %s\n", plugin.ID, plugin.Kind, yesNo(plugin.Enabled), truncateJSON(plugin.Config, 40))
	}
}

func isValidPluginKind(kind string) bool {
	switch kind {
	case config.PluginKindTool, config.PluginKindChannel, config.PluginKindHook:
		return true
	default:
		return false
	}
}

func pluginEnabledVerb(enabled bool) string {
	if enabled {
		return "enable"
	}
	return "disable"
}

func pluginEnabledState(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func truncateJSON(m map[string]any, maxLen int) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

// parseConfigValue tries JSON first (for numbers, bools, objects), falls back to string.
func parseConfigValue(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}
