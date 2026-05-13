package sandbox

import (
	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func init() {
	type meta struct {
		name        string
		displayName string
		description string
	}
	backends := []meta{
		{
			name:        config.SandboxBackendDocker,
			displayName: "Docker",
			description: "Container-per-session isolation via Docker. Requires a running Docker daemon.",
		},
		{
			name:        config.SandboxBackendLocal,
			displayName: "Local",
			description: "Default Docker-free sandbox; uses bwrap on Linux and macOS Seatbelt (sandbox-exec) on macOS for filesystem and network isolation.",
		},
		{
			name:        config.SandboxBackendNone,
			displayName: "None",
			description: "No sandbox. Agent runs directly on the host with the current user's permissions. Use only for trusted workloads.",
		},
	}
	for _, b := range backends {
		id := config.PluginID(config.PluginKindSandbox, b.name)
		name := b.name
		displayName := b.displayName
		description := b.description
		pkgplugins.Register(id, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
			host.SetInfo(pkgplugins.PluginInfo{
				ID:           config.PluginID(config.PluginKindSandbox, name),
				Kind:         config.PluginKindSandbox,
				Name:         name,
				DisplayName:  displayName,
				Description:  description,
				AdminVisible: true,
			})
		}))
	}
}
