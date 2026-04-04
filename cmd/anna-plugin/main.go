package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	ucli "github.com/urfave/cli/v2"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	app := &ucli.App{
		Name:  "anna-plugin",
		Usage: "Internal Anna plugin helper",
		Commands: []*ucli.Command{
			toolCommand(),
			channelCommand(),
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func channelCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "channel",
		Usage: "Run a built-in channel plugin",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "work-dir"},
			&ucli.StringFlag{Name: "user-data-dir"},
		},
		Action: func(c *ucli.Context) error {
			name, flags := extractPluginArgs(c)
			if name == "" {
				return fmt.Errorf("usage: anna-plugin channel <name>")
			}

			workDir := firstNonEmpty(c.String("work-dir"), flags["work-dir"], os.Getenv("ANNA_PLUGIN_WORKDIR"))
			userDataDir := firstNonEmpty(c.String("user-data-dir"), flags["user-data-dir"], os.Getenv("ANNA_PLUGIN_USER_DATA_DIR"))

			def, runtime, err := buildChannelPlugin(name, workDir, userDataDir)
			if err != nil {
				return err
			}

			return pluginhost.ServeChannel(c.Context, def, runtime, os.Stdin, os.Stdout)
		},
	}
}

func toolCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "tool",
		Usage: "Run a built-in tool plugin",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "work-dir"},
			&ucli.StringFlag{Name: "user-data-dir"},
		},
		Action: func(c *ucli.Context) error {
			name, flags := extractPluginArgs(c)
			if name == "" {
				return fmt.Errorf("usage: anna-plugin tool <name>")
			}

			workDir := firstNonEmpty(c.String("work-dir"), flags["work-dir"], os.Getenv("ANNA_PLUGIN_WORKDIR"))
			userDataDir := firstNonEmpty(c.String("user-data-dir"), flags["user-data-dir"], os.Getenv("ANNA_PLUGIN_USER_DATA_DIR"))

			runtime, err := agenttool.BuiltinToolRuntime(name, workDir, userDataDir)
			if err != nil {
				return err
			}

			toolDef := runtime.Definition()
			def := pluginhost.Definition{
				Manifest: pluginapi.Manifest{
					Name:            name,
					Version:         "1.0.0",
					Kind:            pluginapi.KindTool,
					ProtocolVersion: pluginapi.ProtocolVersion,
					Entrypoint:      pluginhost.BuiltinEntrypoint,
					Capabilities: []pluginapi.Capability{
						pluginapi.CapabilityToolCall,
						pluginapi.CapabilityHealthCheck,
						pluginapi.CapabilityGracefulShutdown,
					},
					Tool: &pluginapi.ToolSpec{
						Name:        toolDef.Name,
						Description: toolDef.Description,
						InputSchema: toolDef.InputSchema,
					},
				},
			}

			return pluginhost.ServeTool(c.Context, def, runtime, os.Stdin, os.Stdout)
		},
	}
}

// extractPluginArgs extracts the plugin name and trailing --key=value or
// --key value flags from positional args. urfave/cli v2 stops parsing flags
// after the first positional arg, so flags like --work-dir that appear after
// the plugin name are treated as positional args.
func extractPluginArgs(c *ucli.Context) (string, map[string]string) {
	flags := make(map[string]string)
	var name string
	args := c.Args().Slice()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			key := strings.TrimPrefix(a, "--")
			if k, v, ok := strings.Cut(key, "="); ok {
				flags[k] = v
			} else if i+1 < len(args) {
				i++
				flags[key] = args[i]
			}
		} else if name == "" {
			name = a
		}
	}
	return name, flags
}

// firstNonEmpty returns the first non-empty string from the given values.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
