// Package config currently holds two distinct concepts that share a package
// only for historical reasons (it is a leaf package everything can import
// without cycles). Files are grouped so a future package split is mechanical:
//
// Process/environment configuration — parsed from the environment at the
// server startup boundary and injected into subsystems:
//
//   - server_config.go: ServerConfig, the boot-time env contract (issue #701)
//   - lifecycle.go: graceful-shutdown drain budgets (nested in ServerConfig)
//   - paths.go: STELLA_HOME bootstrap (pre-ServerConfig by necessity)
//   - sandbox_env.go: per-call STELLA_SANDBOX_BACKEND backend selection
//
// DB-backed application configuration — domain types persisted in PostgreSQL
// and served through the Store interface (implemented by internal/store):
//
//   - store.go: the Store interface
//   - provider.go, agent.go, channel.go, plugin.go, models_cache.go: row types
//   - settings.go, embedding.go: app_setting key-value payloads
//   - sandbox.go: per-agent sandbox settings (part of Agent)
//   - snapshot.go: the assembled per-agent runtime view and its model/creds
//     resolution
//
// New process/env variables belong on ServerConfig (env_scan_test.go enforces
// this); new DB-backed types belong in their own file on the second group.
package config
