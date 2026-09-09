package config

import "time"

// SandboxMaintenanceConfig contains only inputs used by the operator command.
// Invalid model, channel, or server settings must not prevent resource recovery.
type SandboxMaintenanceConfig struct {
	Database          DatabaseConfig
	KubernetesSandbox KubernetesSandboxConfig
}

func LoadSandboxMaintenanceConfig(lookup func(string) (string, bool), controllers bool) (SandboxMaintenanceConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	var cfg SandboxMaintenanceConfig
	cfg.Database.URL = get(databaseURLEnv)
	requireExternal, err := parseServerBool(requireExternalDBEnv, get(requireExternalDBEnv))
	if err != nil {
		return cfg, err
	}
	cfg.Database.RequireExternalDB = requireExternal
	if !controllers {
		return cfg, nil
	}
	cfg.KubernetesSandbox.Image = get("STELLA_KUBERNETES_IMAGE")
	cfg.KubernetesSandbox.ServerURL = get("STELLA_SANDBOX_SERVER_URL")
	cfg.KubernetesSandbox.StartupTimeout, err = parseServerDuration("STELLA_KUBERNETES_STARTUP_TIMEOUT", get("STELLA_KUBERNETES_STARTUP_TIMEOUT"), 120*time.Second)
	return cfg, err
}
