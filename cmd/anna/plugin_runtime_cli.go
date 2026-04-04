package main

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v2"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginhost"
)

func runtimePluginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "runtime",
		Usage: "Manage subprocess runtime plugins",
		Subcommands: []*ucli.Command{
			runtimePluginListCommand(),
			runtimePluginBindCommand(),
		},
		Action: runtimePluginListAction,
	}
}

func runtimePluginListCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "list",
		Usage:  "List effective runtime plugin bindings for tools and channels",
		Action: runtimePluginListAction,
	}
}

func runtimePluginListAction(c *ucli.Context) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	snap, err := defaultSnapshot(c.Context, store)
	if err != nil {
		return err
	}
	bindings, err := config.LoadRuntimePluginBindings(store)
	if err != nil {
		return err
	}

	toolCatalog, err := agenttool.LoadCatalog(snap.Workspace, config.AnnaHome())
	if err != nil {
		return err
	}
	channelCatalog, err := loadRuntimeChannelCatalog(snap.Workspace, config.AnnaHome())
	if err != nil {
		return err
	}

	fmt.Println("Runtime tool bindings:")
	fmt.Printf("%-10s %-24s %-10s %s\n", "SLOT", "PLUGIN", "SOURCE", "VERSION")
	for _, name := range agenttool.BuiltinToolNames() {
		id := bindings.ToolBinding(name)
		def, _ := toolCatalog.Get(id)
		fmt.Printf("%-10s %-24s %-10s %s\n", name, id, runtimePluginSource(def), def.Manifest.Version)
	}

	fmt.Println()
	fmt.Println("Runtime channel bindings:")
	fmt.Printf("%-10s %-24s %-10s %-8s %s\n", "SLOT", "PLUGIN", "SOURCE", "ENABLED", "VERSION")
	channelEnabled, err := enabledChannels(c.Context, store)
	if err != nil {
		return err
	}
	for _, name := range channel.BuiltinChannelNames() {
		id := bindings.ChannelBinding(name)
		def, _ := channelCatalog.Get(id)
		fmt.Printf("%-10s %-24s %-10s %-8s %s\n", name, id, runtimePluginSource(def), yesNo(channelEnabled[name]), def.Manifest.Version)
	}

	fmt.Printf("\nLogs: %s\n", config.AnnaHome()+"/anna.log")
	return nil
}

func runtimePluginBindCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "bind",
		Usage:     "Bind a tool or channel slot to a runtime plugin ID",
		ArgsUsage: "<tool|channel> <slot> [plugin-id]",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:  "default",
				Usage: "Reset the slot back to its default bundled plugin",
			},
		},
		Action: runtimePluginBindAction,
	}
}

func runtimePluginBindAction(c *ucli.Context) error {
	// urfave/cli v2 stops parsing flags after the first positional arg,
	// so --default appearing after positional args is treated as a positional arg.
	// Manually extract it from the args.
	useDefault := c.Bool("default")
	var positional []string
	for _, a := range c.Args().Slice() {
		if a == "--default" {
			useDefault = true
		} else {
			positional = append(positional, a)
		}
	}

	var kind, slot, pluginID string
	if len(positional) > 0 {
		kind = positional[0]
	}
	if len(positional) > 1 {
		slot = positional[1]
	}
	if len(positional) > 2 {
		pluginID = positional[2]
	}
	if kind == "" || slot == "" {
		return fmt.Errorf("usage: anna plugin runtime bind <tool|channel> <slot> [plugin-id]")
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	snap, err := defaultSnapshot(c.Context, store)
	if err != nil {
		return err
	}
	bindings, err := config.LoadRuntimePluginBindings(store)
	if err != nil {
		return err
	}

	switch kind {
	case "tool":
		if !contains(agenttool.BuiltinToolNames(), slot) {
			return fmt.Errorf("unknown tool slot %q", slot)
		}
		if useDefault {
			pluginID = config.DefaultRuntimePluginBindings().ToolBinding(slot)
		}
		if pluginID == "" {
			return fmt.Errorf("plugin id is required unless --default is set")
		}
		catalog, err := agenttool.LoadCatalog(snap.Workspace, config.AnnaHome())
		if err != nil {
			return err
		}
		if err := validateRuntimeBinding(catalog, pluginID, "tool"); err != nil {
			return err
		}
		if bindings.Tools == nil {
			bindings.Tools = map[string]string{}
		}
		bindings.Tools[slot] = pluginID

	case "channel":
		if !contains(channel.BuiltinChannelNames(), slot) {
			return fmt.Errorf("unknown channel slot %q", slot)
		}
		if useDefault {
			pluginID = config.DefaultRuntimePluginBindings().ChannelBinding(slot)
		}
		if pluginID == "" {
			return fmt.Errorf("plugin id is required unless --default is set")
		}
		catalog, err := loadRuntimeChannelCatalog(snap.Workspace, config.AnnaHome())
		if err != nil {
			return err
		}
		if err := validateRuntimeBinding(catalog, pluginID, "channel"); err != nil {
			return err
		}
		if bindings.Channels == nil {
			bindings.Channels = map[string]string{}
		}
		bindings.Channels[slot] = pluginID

	default:
		return fmt.Errorf("kind must be tool or channel")
	}

	if err := config.SaveRuntimePluginBindings(store, bindings); err != nil {
		return err
	}

	fmt.Printf("Bound %s %q to %s.\n", kind, slot, pluginID)
	return nil
}

func validateRuntimeBinding(catalog *pluginhost.Catalog, pluginID, kind string) error {
	def, ok := catalog.Get(pluginID)
	if !ok {
		return fmt.Errorf("runtime plugin %q not found", pluginID)
	}
	if string(def.Manifest.Kind) != kind {
		return fmt.Errorf("runtime plugin %q has kind %q, want %q", pluginID, def.Manifest.Kind, kind)
	}
	return nil
}

func enabledChannels(ctx context.Context, store config.Store) (map[string]bool, error) {
	rows, err := store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, ch := range rows {
		out[ch.ID] = ch.Enabled
	}
	return out, nil
}

func runtimePluginSource(def pluginhost.Definition) string {
	if def.Manifest.Entrypoint == pluginhost.BuiltinEntrypoint {
		return "bundled"
	}
	if def.ManifestPath == "" {
		return "runtime"
	}
	return "installed"
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
