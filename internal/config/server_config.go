package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment variable names owned by ServerConfig. Struct tags on
// rawServerConfig must use these exact literals; the constants are the single
// source of truth for the normalized-environment key set and the parse-error
// messages.
const (
	requireExternalDBEnv    = "STELLA_REQUIRE_EXTERNAL_DB"
	databaseURLEnv          = "STELLA_DATABASE_URL"
	httpShutdownTimeoutEnv  = "STELLA_HTTP_SHUTDOWN_TIMEOUT"
	riverSoftStopTimeoutEnv = "STELLA_RIVER_SOFT_STOP_TIMEOUT"
	serverURLEnv            = "STELLA_SERVER_URL"
)

// ServerConfig is the boot-time-static environment configuration the stella
// server parses once, at its startup boundary (serverAction), and threads into
// the subsystems that need it. It is deliberately NOT read on the hot path:
// parse it once and inject the values, so a misconfiguration fails fast at
// startup rather than surfacing mid-request.
//
// PR1 (issue #701) carries only the five variables whose reads already lived in
// internal/config and are boot-time static. Consumer packages (blob, oidc,
// observability, reflect, ...) migrate onto this struct in PR2; see
// docs/design/research/2026-07-11-env-inventory.md for the full ledger and the
// per-field semantics that block a purely mechanical migration.
type ServerConfig struct {
	// Database selects between the embedded PostgreSQL convenience cluster and an
	// external server.
	Database DatabaseConfig
	// Lifecycle bounds the two-phase graceful-shutdown drain. It is the same type
	// injected since #700; keep it a nested group so that injection path is
	// unchanged.
	Lifecycle Lifecycle
	// ServerURL is the URL CLI commands use to reach the local stella server.
	// It has no Go consumer today (the sandbox injects STELLA_SERVER_URL into the
	// container itself); the field is carried so a future CLI client threads it
	// from here instead of reading the environment directly.
	ServerURL string
}

// DatabaseConfig holds the two variables that jointly decide, at startup,
// whether the server runs its own embedded PostgreSQL or connects to an
// external one.
type DatabaseConfig struct {
	// RequireExternalDB forbids the embedded-PostgreSQL fallback. The embedded
	// cluster is a single-node local convenience: in a container it lands on an
	// ephemeral filesystem, and with multiple replicas each process would create
	// its own database. The Docker image sets this by default; set it to 0 to
	// deliberately run embedded PostgreSQL inside a container (e.g. a single
	// container with a persistent volume). Unset or empty is false; any other
	// value is parsed as a boolean and an unparseable value fails fast.
	RequireExternalDB bool
	// URL is an explicitly configured PostgreSQL DSN (STELLA_DATABASE_URL), or ""
	// when none is set. An empty result tells the server to start and manage its
	// own embedded PostgreSQL — the zero-config default, so a fresh install needs
	// no separately installed or running database.
	URL string
}

// rawServerConfig is the wire format env.ParseWithOptions fills: every field is
// a trimmed string. Duration and bool fields keep custom, env-name-aware
// validation (see convert below) because the env library wraps parser errors
// with the Go field name and cannot enforce the ">0" bound — so we parse those
// after the struct is populated to preserve the existing actionable messages
// and to keep the DSN out of any error text.
type rawServerConfig struct {
	RequireExternalDB    string `env:"STELLA_REQUIRE_EXTERNAL_DB"`
	DatabaseURL          string `env:"STELLA_DATABASE_URL"`
	HTTPShutdownTimeout  string `env:"STELLA_HTTP_SHUTDOWN_TIMEOUT"`
	RiverSoftStopTimeout string `env:"STELLA_RIVER_SOFT_STOP_TIMEOUT"`
	ServerURL            string `env:"STELLA_SERVER_URL" envDefault:"http://127.0.0.1:25678"`
}

// serverConfigKeys is the closed set of variables ServerConfig owns. The
// normalized environment is built from exactly these keys so an unrelated
// variable can never leak into the parse.
var serverConfigKeys = []string{
	requireExternalDBEnv,
	databaseURLEnv,
	httpShutdownTimeoutEnv,
	riverSoftStopTimeoutEnv,
	serverURLEnv,
}

// LoadServerConfig parses the server's boot-time environment. lookup resolves a
// variable name to its value and presence (os.LookupEnv in production); passing
// it in keeps the function pure and lets tests drive it without mutating process
// state.
//
// Normalization (all fields): the value is trimmed, and a whitespace-only or
// empty value is treated as unset so the default applies — preserving the
// current TrimSpace-then-default behavior. Parse errors for every field are
// aggregated (env.AggregateError) so a misconfigured deployment sees all its
// mistakes at once, not one per restart.
func LoadServerConfig(lookup func(string) (string, bool)) (ServerConfig, error) {
	environment := make(map[string]string, len(serverConfigKeys))
	for _, k := range serverConfigKeys {
		if raw, ok := lookup(k); ok {
			if v := strings.TrimSpace(raw); v != "" {
				environment[k] = v
			}
		}
	}

	var raw rawServerConfig
	if err := env.ParseWithOptions(&raw, env.Options{Environment: environment}); err != nil {
		// String fields cannot fail to parse; this guards against a future typed
		// field regressing silently.
		return ServerConfig{}, err
	}
	return raw.convert()
}

// convert turns the parsed strings into the typed, validated ServerConfig,
// aggregating every field error rather than stopping at the first.
func (raw rawServerConfig) convert() (ServerConfig, error) {
	var errs []error

	requireExternalDB, err := parseServerBool(requireExternalDBEnv, raw.RequireExternalDB)
	if err != nil {
		errs = append(errs, err)
	}
	httpTimeout, err := parseServerDuration(httpShutdownTimeoutEnv, raw.HTTPShutdownTimeout, defaultHTTPShutdownTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	riverTimeout, err := parseServerDuration(riverSoftStopTimeoutEnv, raw.RiverSoftStopTimeout, defaultRiverSoftStopTimeout)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return ServerConfig{}, env.AggregateError{Errors: errs}
	}
	return ServerConfig{
		Database: DatabaseConfig{
			RequireExternalDB: requireExternalDB,
			URL:               raw.DatabaseURL,
		},
		Lifecycle: Lifecycle{
			HTTPShutdownTimeout:  httpTimeout,
			RiverSoftStopTimeout: riverTimeout,
		},
		ServerURL: raw.ServerURL,
	}, nil
}

// parseServerBool parses a normalized boolean value. An empty value (unset after
// normalization) is false; an unparseable value fails fast with actionable
// guidance instead of being silently ignored, so a typo cannot quietly disable a
// guard.
func parseServerBool(name, v string) (bool, error) {
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: set it to 1/true or 0/false", name, v)
	}
	return b, nil
}

// parseServerDuration parses a normalized Go duration (time.ParseDuration),
// returning def for an empty value (unset after normalization). It rejects
// unparseable and non-positive values with an actionable error instead of
// silently falling back, so a typo fails fast.
func parseServerDuration(name, v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a valid duration: use a Go duration such as 60s, 2m, or 500ms", name, v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s=%q must be greater than zero", name, v)
	}
	return d, nil
}

// Process-wide snapshot. Installed once at startup and never reloaded in
// production: the parsed config is threaded into subsystems (see serverAction),
// and this snapshot is the read-only fallback for consumers that PR2 migrates
// but cannot yet reach via injection. Production accessors read it and never
// fall back to os.Getenv.
var (
	serverConfigMu        sync.RWMutex
	serverConfigInstalled bool
	serverConfigValue     ServerConfig
)

// InstallServerConfig publishes the parsed config as the process-wide snapshot.
// It may be called at most once, at server startup; a second call is a
// programming error (two startup paths racing to own the config) and returns an
// error rather than silently clobbering the first.
func InstallServerConfig(cfg ServerConfig) error {
	serverConfigMu.Lock()
	defer serverConfigMu.Unlock()
	if serverConfigInstalled {
		return errors.New("server config already installed")
	}
	serverConfigInstalled = true
	serverConfigValue = cfg
	return nil
}

// InstalledServerConfig returns the installed snapshot, panicking if
// InstallServerConfig has not run. It never reads the environment, so a caller
// that reaches it before startup installs the config fails loudly instead of
// silently diverging from the parsed values.
func InstalledServerConfig() ServerConfig {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()
	if !serverConfigInstalled {
		panic("config: server config not installed; call InstallServerConfig at startup")
	}
	return serverConfigValue
}
