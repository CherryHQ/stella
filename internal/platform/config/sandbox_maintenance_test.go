package config

import "testing"

func TestSandboxMaintenanceDoesNotParseUnrelatedServerSettings(t *testing.T) {
	env := map[string]string{
		"STELLA_DATABASE_URL":               "postgres://example.invalid/stella",
		"STELLA_REQUIRE_EXTERNAL_DB":        "true",
		"STELLA_SHUTDOWN_TIMEOUT":           "invalid-server-duration",
		"STELLA_KUBERNETES_STARTUP_TIMEOUT": "invalid-backend-duration",
	}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	cfg, err := LoadSandboxMaintenanceConfig(lookup, false)
	if err != nil {
		t.Fatalf("inspection rejected unrelated configuration: %v", err)
	}
	if cfg.Database.URL != env["STELLA_DATABASE_URL"] || !cfg.Database.RequireExternalDB {
		t.Fatalf("database configuration = %+v", cfg.Database)
	}
	if _, err := LoadSandboxMaintenanceConfig(lookup, true); err == nil {
		t.Fatal("controller configuration accepted invalid backend timeout")
	}
	delete(env, "STELLA_KUBERNETES_STARTUP_TIMEOUT")
	if _, err := LoadSandboxMaintenanceConfig(lookup, true); err != nil {
		t.Fatalf("reconciliation rejected unrelated server configuration: %v", err)
	}
}
