package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
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

	ctx := context.Background()
	kind := c.String("kind")

	var plugins []config.Plugin
	if kind != "" {
		if kind != config.PluginKindTool && kind != config.PluginKindChannel && kind != config.PluginKindHook {
			return fmt.Errorf("invalid kind %q, must be %q, %q, or %q", kind, config.PluginKindTool, config.PluginKindChannel, config.PluginKindHook)
		}
		plugins, err = store.ListPluginsByKind(ctx, kind)
	} else {
		plugins, err = store.ListPlugins(ctx)
	}
	if err != nil {
		return err
	}

	if len(plugins) == 0 {
		fmt.Println("No plugins configured.")
		return nil
	}

	fmt.Printf("%-24s %-10s %-8s %s\n", "ID", "KIND", "ENABLED", "CONFIG")
	for _, p := range plugins {
		fmt.Printf("%-24s %-10s %-8s %s\n", p.ID, p.Kind, yesNo(p.Enabled), truncateJSON(p.Config, 40))
	}
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
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return fmt.Errorf("usage: anna plugin %s <id>", verb)
	}

	store, err := openStore()
	if err != nil {
		return err
	}

	ctx := context.Background()
	if _, err := store.GetPlugin(ctx, id); err != nil {
		return fmt.Errorf("plugin %q not found", id)
	}

	if err := store.SetPluginEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("update plugin: %w", err)
	}

	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Plugin %q %s.\n", id, state)
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
	p, err := store.GetPlugin(ctx, id)
	if err != nil {
		return fmt.Errorf("plugin %q not found", id)
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
