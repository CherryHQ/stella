package channel

import (
	"fmt"
	"path/filepath"

	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

const builtinChannelPluginVersion = "1.0.0"

func BuiltinChannelDefinitions(workDir, userDataDir string) []pluginhost.Definition {
	defs := make([]pluginhost.Definition, 0, 4)
	for _, name := range []string{"telegram", "qq", "feishu", "weixin"} {
		def, err := BuiltinChannelDefinition(name, workDir, userDataDir)
		if err != nil {
			continue
		}
		defs = append(defs, def)
	}
	return defs
}

func BuiltinChannelDefinition(name, workDir, userDataDir string) (pluginhost.Definition, error) {
	var description string
	switch name {
	case "telegram":
		description = "Telegram channel plugin"
	case "qq":
		description = "QQ channel plugin"
	case "feishu":
		description = "Feishu channel plugin"
	case "weixin":
		description = "Weixin channel plugin"
	default:
		return pluginhost.Definition{}, fmt.Errorf("unknown builtin channel plugin: %s", name)
	}

	manifest := pluginapi.Manifest{
		Name:            name,
		Version:         builtinChannelPluginVersion,
		Kind:            pluginapi.KindChannel,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      pluginhost.BuiltinEntrypoint,
		Args: []string{
			"channel",
			name,
		},
		Description: description,
		Capabilities: []pluginapi.Capability{
			pluginapi.CapabilityChannelStart,
			pluginapi.CapabilityChannelStop,
			pluginapi.CapabilityChannelNotify,
			pluginapi.CapabilityChannelInbound,
			pluginapi.CapabilityHealthCheck,
			pluginapi.CapabilityGracefulShutdown,
		},
		Metadata: map[string]any{
			"work_dir":      workDir,
			"user_data_dir": userDataDir,
		},
	}
	if workDir != "" {
		manifest.Args = append(manifest.Args, "--work-dir", workDir)
	}
	if userDataDir != "" {
		manifest.Args = append(manifest.Args, "--user-data-dir", userDataDir)
	}

	rootDir := workDir
	if rootDir == "" {
		rootDir = userDataDir
	}
	if rootDir == "" {
		rootDir = "."
	}

	return pluginhost.Definition{
		Manifest: manifest,
		RootDir:  filepath.Clean(rootDir),
	}, nil
}
