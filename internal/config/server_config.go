package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment variable names owned by ServerConfig. Struct tags on
// rawServerConfig must use these exact literals; the constants are the single
// source of truth for the normalized-environment key set and the parse-error
// messages.
const (
	requireExternalDBEnv    = "STELLA_REQUIRE_EXTERNAL_DB"
	httpShutdownTimeoutEnv  = "STELLA_HTTP_SHUTDOWN_TIMEOUT"
	riverSoftStopTimeoutEnv = "STELLA_RIVER_SOFT_STOP_TIMEOUT"

	// Raw passthrough vars: read with os.Getenv semantics (value or "" for
	// unset/empty; no trim, no default). Their group-level validation stays with
	// the consuming subsystem (blob group constraint, oidc Validate, vault
	// required check) so semantics are preserved exactly. The two URLs are here
	// deliberately: the legacy readers returned them untrimmed, so normalizing
	// them would silently rewrite a padded DSN instead of letting the database
	// layer reject it.
	databaseURLEnv  = "STELLA_DATABASE_URL"
	serverURLEnv    = "STELLA_SERVER_URL"
	baseURLEnv      = "STELLA_BASE_URL"
	vaultKeyEnv     = "STELLA_VAULT_KEY"
	pprofAddrEnv    = "STELLA_PPROF_ADDR"
	recordToolIOEnv = "OTEL_STELLA_RECORD_TOOL_IO"
	// riverLogLevelEnv is the companion of LOG_LEVEL (read pre-config in main)
	// for the River job queue only; internal/cli.ParseLogLevel owns the dialect,
	// so the value passes through raw.
	riverLogLevelEnv = "LOG_LEVEL_RIVER"

	reflectIntervalEnv    = "STELLA_REFLECT_INTERVAL"
	reflectModeEnv        = "STELLA_REFLECT_MODE"
	reflectCuratorModeEnv = "STELLA_REFLECT_CURATOR_MODE"

	groupMemoryModeEnv      = "STELLA_GROUP_MEMORY_MODE"
	groupReflectModelEnv    = "STELLA_GROUP_REFLECT_MODEL"
	groupReflectIntervalEnv = "STELLA_GROUP_REFLECT_INTERVAL"

	oidcProviderNameEnv = "OIDC_PROVIDER_NAME"
	oidcIssuerURLEnv    = "OIDC_ISSUER_URL"
	oidcClientIDEnv     = "OIDC_CLIENT_ID"
	oidcClientSecretEnv = "OIDC_CLIENT_SECRET"
	oidcRedirectURLEnv  = "OIDC_REDIRECT_URL"
	oidcScopesEnv       = "OIDC_SCOPES"

	blobS3EndpointEnv  = "STELLA_BLOB_S3_ENDPOINT"
	blobS3BucketEnv    = "STELLA_BLOB_S3_BUCKET"
	blobS3AccessKeyEnv = "STELLA_BLOB_S3_ACCESS_KEY"
	blobS3SecretKeyEnv = "STELLA_BLOB_S3_SECRET_KEY"
	blobS3RegionEnv    = "STELLA_BLOB_S3_REGION"
	blobS3UseSSLEnv    = "STELLA_BLOB_S3_USE_SSL"
)

// ServerConfig is the boot-time-static environment configuration the stella
// server parses once, at its startup boundary (serverAction), and threads into
// the subsystems that need it. It is deliberately NOT read on the hot path:
// parse it once and inject the values, so a misconfiguration fails fast at
// startup rather than surfacing mid-request.
//
// It carries every boot/setup-time environment variable the server owns (issue
// #701). Variables that are read per-call, live in pkg/ (which must not import
// this package), or need dialect/validation the consuming subsystem owns are
// NOT here; the allowlist in env_scan_test.go enumerates every such exception
// with the reason it stays a direct read.
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
	// BaseURL is the externally reachable base URL of the deployment
	// (STELLA_BASE_URL), raw and un-normalized: "" means "derive from the bind
	// host". The gateway trims a trailing slash where it builds absolute URLs;
	// the raw value is carried so that trimming stays at the use site.
	BaseURL string
	// Vault carries the master key used to seal per-user secrets.
	Vault VaultConfig
	// OIDC is the static single-provider OIDC block. It is read raw (no trim) so
	// the external-vs-local mode decision, the provider config, and the
	// dependent-feature check all observe one snapshot with no os.Getenv/config
	// generation split.
	OIDC OIDCConfig
	// Blob holds the raw S3-compatible blob-store settings. The group constraint
	// (any set => the four core fields required) and the USE_SSL bool dialect stay
	// in the blob package, which consumes these raw strings.
	Blob BlobS3Config
	// Reflect carries the raw reflect-scheduler tuning strings. Parsing stays in
	// the reflect setup so its lenient-warn-and-clamp interval behavior and
	// fail-fast curator-mode enum are preserved exactly.
	Reflect ReflectConfig
	// GroupMemory carries the controlled legacy/structured rollout settings.
	// The Group Reflect setup owns mode, model, and interval validation.
	GroupMemory GroupMemoryConfig
	// Diagnostics holds optional debug-server settings.
	Diagnostics DiagnosticsConfig
	// Observability holds tracing/telemetry toggles owned by this server (the
	// standard OTEL SDK variables stay with the SDK and are not mirrored here).
	Observability ObservabilityConfig
}

// VaultConfig carries the vault master key (STELLA_VAULT_KEY). The key is a
// secret: it must never appear in any error or log text, so this struct is not
// logged and callers thread Key only into the vault service. The value is raw
// (os.Getenv semantics, no trim) to preserve the exact "empty => required
// error" check at the server startup boundary.
type VaultConfig struct {
	Key string
}

// OIDCConfig is the static single-provider OIDC block (OIDC_* variables), read
// raw. ClientSecret is a secret and must never be logged. Scopes is the raw
// OIDC_SCOPES string; the oidc package owns splitting and defaulting it so the
// parsing stays next to its use.
type OIDCConfig struct {
	ProviderName string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
}

// BlobS3Config holds the six raw S3 blob-store variables. The blob package owns
// the group constraint and the USE_SSL dialect, so these stay untyped strings
// here.
type BlobS3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    string
}

// ReflectConfig carries raw reflect-scheduler settings. LegacyModeGuard is only
// used to reject stale STELLA_REFLECT_MODE values during the structured-only
// transition; it does not select a writer.
type ReflectConfig struct {
	Interval        string
	LegacyModeGuard string
	CuratorMode     string
}

type GroupMemoryConfig struct {
	Mode            string
	ReflectModel    string
	ReflectInterval string
}

// DiagnosticsConfig holds optional local debug-server settings.
type DiagnosticsConfig struct {
	// PprofAddr is the listen address for the net/http/pprof debug server
	// (STELLA_PPROF_ADDR). Empty (unset) leaves the debug server disabled. Raw
	// (os.Getenv semantics) so the disabled-when-empty check is unchanged.
	PprofAddr string
}

// ObservabilityConfig holds tracing and logging toggles this server owns.
type ObservabilityConfig struct {
	// RecordToolIO records tool input/output payloads on spans
	// (OTEL_STELLA_RECORD_TOOL_IO). Preserves the exact ==\"true\" opt-in: any
	// other value, including unset, leaves it off.
	RecordToolIO bool
	// RiverLogLevel is the raw LOG_LEVEL_RIVER value ("" for unset). The River
	// job queue heartbeats at DEBUG/INFO, so its logger is capped at WARN unless
	// this opens it up; internal/cli.ParseLogLevel owns the level dialect.
	RiverLogLevel string
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

// defaultServerURL is the legacy ServerURL fallback: applied only when the
// variable is unset or exactly empty, never to a non-empty value (which is
// passed through untrimmed).
const defaultServerURL = "http://127.0.0.1:25678"

// rawServerConfig is the wire format env.ParseWithOptions fills: every field is
// a trimmed string. Duration and bool fields keep custom, env-name-aware
// validation (see convert below) because the env library wraps parser errors
// with the Go field name and cannot enforce the ">0" bound — so we parse those
// after the struct is populated to preserve the existing actionable messages.
type rawServerConfig struct {
	RequireExternalDB    string `env:"STELLA_REQUIRE_EXTERNAL_DB"`
	HTTPShutdownTimeout  string `env:"STELLA_HTTP_SHUTDOWN_TIMEOUT"`
	RiverSoftStopTimeout string `env:"STELLA_RIVER_SOFT_STOP_TIMEOUT"`
}

// serverConfigKeys is the closed set of normalized (trimmed, empty=default)
// variables: exactly the typed fields whose legacy parsers TrimSpace'd their
// input. The normalized environment is built from exactly these keys so an
// unrelated variable can never leak into the parse.
var serverConfigKeys = []string{
	requireExternalDBEnv,
	httpShutdownTimeoutEnv,
	riverSoftStopTimeoutEnv,
}

// LoadServerConfig parses the server's boot-time environment. lookup resolves a
// variable name to its value and presence (os.LookupEnv in production); passing
// it in keeps the function pure and lets tests drive it without mutating process
// state.
//
// Normalization (typed fields only, serverConfigKeys): the value is trimmed,
// and a whitespace-only or empty value is treated as unset so the default
// applies — preserving the legacy TrimSpace-then-default parsers. String
// passthrough fields (URLs, secrets, subsystem-validated blocks) are exempt and
// keep exact os.Getenv semantics. Parse errors for every field are aggregated
// (env.AggregateError) so a misconfigured deployment sees all its mistakes at
// once, not one per restart.
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
	cfg, err := raw.convert()
	if err != nil {
		return ServerConfig{}, err
	}

	// Raw passthrough fields: read with os.Getenv semantics (value or "" for
	// unset/empty; no trim, no default). Their group-level validation lives with
	// the consuming subsystem, so loading never fails here and semantics are
	// preserved exactly. A secret (Vault.Key, OIDC.ClientSecret) is only stored,
	// never logged.
	get := func(name string) string { v, _ := lookup(name); return v }
	cfg.Database.URL = get(databaseURLEnv)
	// Legacy ServerURL rule: default only replaces unset/exactly-empty; any
	// non-empty value (even whitespace) passes through untouched.
	if cfg.ServerURL = get(serverURLEnv); cfg.ServerURL == "" {
		cfg.ServerURL = defaultServerURL
	}
	cfg.BaseURL = get(baseURLEnv)
	cfg.Vault.Key = get(vaultKeyEnv)
	cfg.OIDC = OIDCConfig{
		ProviderName: get(oidcProviderNameEnv),
		IssuerURL:    get(oidcIssuerURLEnv),
		ClientID:     get(oidcClientIDEnv),
		ClientSecret: get(oidcClientSecretEnv),
		RedirectURL:  get(oidcRedirectURLEnv),
		Scopes:       get(oidcScopesEnv),
	}
	cfg.Blob = BlobS3Config{
		Endpoint:  get(blobS3EndpointEnv),
		Bucket:    get(blobS3BucketEnv),
		AccessKey: get(blobS3AccessKeyEnv),
		SecretKey: get(blobS3SecretKeyEnv),
		Region:    get(blobS3RegionEnv),
		UseSSL:    get(blobS3UseSSLEnv),
	}
	cfg.Reflect = ReflectConfig{
		Interval:        get(reflectIntervalEnv),
		LegacyModeGuard: get(reflectModeEnv),
		CuratorMode:     get(reflectCuratorModeEnv),
	}
	cfg.GroupMemory = GroupMemoryConfig{
		Mode:            get(groupMemoryModeEnv),
		ReflectModel:    get(groupReflectModelEnv),
		ReflectInterval: get(groupReflectIntervalEnv),
	}
	cfg.Diagnostics.PprofAddr = get(pprofAddrEnv)
	cfg.Observability.RecordToolIO = get(recordToolIOEnv) == "true"
	cfg.Observability.RiverLogLevel = get(riverLogLevelEnv)
	return cfg, nil
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
		},
		Lifecycle: Lifecycle{
			HTTPShutdownTimeout:  httpTimeout,
			RiverSoftStopTimeout: riverTimeout,
		},
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

// There is deliberately NO process-wide snapshot of ServerConfig: every
// consumer receives its values by injection from serverAction. A global
// read-only copy (write-once install + accessor) was tried and removed — it had
// zero consumers and kept secrets alive in package state. Reintroduce one only
// when a consumer genuinely cannot be reached by injection.
