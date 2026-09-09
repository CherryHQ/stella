package config

import "time"

// MaintenanceConfig contains only inputs used by operator maintenance commands.
// Invalid model, channel, or server settings must not prevent resource recovery.
type MaintenanceConfig struct {
	Database          DatabaseConfig
	KubernetesSandbox KubernetesSandboxConfig
}

func LoadMaintenanceConfig(lookup func(string) (string, bool), controllers bool) (MaintenanceConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	var cfg MaintenanceConfig
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
