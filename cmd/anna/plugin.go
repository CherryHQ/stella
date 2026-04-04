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
)

func pluginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "plugin",
		Usage: "Manage plugins",
		Subcommands: []*ucli.Command{
			pluginListCommand(),
			pluginAddCommand(),
			pluginRemoveCommand(),
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
		if kind != config.PluginKindTool && kind != config.PluginKindChannel {
			return fmt.Errorf("invalid kind %q, must be %q or %q", kind, config.PluginKindTool, config.PluginKindChannel)
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

// --- add ---

func pluginAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Add a plugin from a local directory",
		ArgsUsage: "<path>",
		Action:    pluginAddAction,
	}
}

func pluginAddAction(c *ucli.Context) error {
	srcPath := c.Args().First()
	if srcPath == "" {
		return fmt.Errorf("usage: anna plugin add <path>")
	}

	srcPath, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	manifest, err := loadPluginManifest(srcPath)
	if err != nil {
		return err
	}

	kind, ok := manifest["kind"].(string)
	if !ok || kind == "" {
		return fmt.Errorf("plugin.json missing required field \"kind\"")
	}
	name, ok := manifest["name"].(string)
	if !ok || name == "" {
		return fmt.Errorf("plugin.json missing required field \"name\"")
	}

	if kind != config.PluginKindTool && kind != config.PluginKindChannel {
		return fmt.Errorf("invalid plugin kind %q, must be %q or %q", kind, config.PluginKindTool, config.PluginKindChannel)
	}

	// Copy to installed plugins directory.
	destDir := filepath.Join(config.InstalledPluginsPath(), kind, name)
	if err := copyDir(srcPath, destDir); err != nil {
		return fmt.Errorf("copy plugin: %w", err)
	}

	// Upsert DB row.
	store, err := openStore()
	if err != nil {
		return err
	}

	pluginID := config.PluginID(kind, name)
	if err := store.UpsertPlugin(context.Background(), config.Plugin{
		ID:      pluginID,
		Kind:    kind,
		Name:    name,
		Enabled: true,
		Config:  map[string]any{},
	}); err != nil {
		return fmt.Errorf("upsert plugin: %w", err)
	}

	fmt.Printf("Plugin %q added and enabled.\n", pluginID)
	return nil
}

// --- remove ---

func pluginRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Remove an installed plugin",
		ArgsUsage: "<id>",
		Action:    pluginRemoveAction,
	}
}

func pluginRemoveAction(c *ucli.Context) error {
	id := c.Args().First()
	if id == "" {
		return fmt.Errorf("usage: anna plugin remove <id>")
	}

	if isBuiltinPlugin(id) {
		return fmt.Errorf("cannot remove built-in plugin %q", id)
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

	// Delete from DB.
	if err := store.DeletePlugin(ctx, id); err != nil {
		return fmt.Errorf("delete plugin: %w", err)
	}

	// Remove from filesystem.
	installDir := filepath.Join(config.InstalledPluginsPath(), p.Kind, p.Name)
	if err := os.RemoveAll(installDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to remove plugin directory %s: %v\n", installDir, err)
	}

	fmt.Printf("Plugin %q removed.\n", id)
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

func isBuiltinPlugin(id string) bool {
	for _, bid := range config.BuiltinPluginIDs() {
		if bid == id {
			return true
		}
	}
	return false
}

// parseConfigValue tries JSON first (for numbers, bools, objects), falls back to string.
func parseConfigValue(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

// loadPluginManifest reads and validates plugin.json at the given directory.
func loadPluginManifest(dir string) (map[string]any, error) {
	manifestPath := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("plugin.json not found at %s: %w", dir, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid plugin.json: %w", err)
	}
	return manifest, nil
}

// copyDir copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
