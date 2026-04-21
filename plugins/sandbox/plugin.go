package sandbox

import (
	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func init() {
	type meta struct {
		name        string
		displayName string
		description string
	}
	backends := []meta{
		{
			name:        config.SandboxBackendAuto,
			displayName: "Auto",
			description: "Selects the best available sandbox backend automatically (boxsh on Linux/macOS, docker elsewhere).",
		},
		{
			name:        config.SandboxBackendBoxsh,
			displayName: "Boxsh",
			description: "Sandboxed execution via boxsh. Supported on Linux and macOS.",
		},
		{
			name:        config.SandboxBackendDocker,
			displayName: "Docker",
			description: "Container-per-session isolation via Docker. Requires a running Docker daemon.",
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
