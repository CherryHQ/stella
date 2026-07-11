package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

// lookupFrom builds a lookup func over a fixed map: a present key (even with an
// empty value) reports ok=true, mirroring os.LookupEnv. Keys absent from the map
// are unset.
func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadServerConfigDefaults(t *testing.T) {
	cfg, err := LoadServerConfig(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadServerConfig() unexpected error: %v", err)
	}
	if cfg.Lifecycle.HTTPShutdownTimeout != defaultHTTPShutdownTimeout {
		t.Errorf("HTTPShutdownTimeout = %v, want %v", cfg.Lifecycle.HTTPShutdownTimeout, defaultHTTPShutdownTimeout)
	}
	if cfg.Lifecycle.RiverSoftStopTimeout != defaultRiverSoftStopTimeout {
		t.Errorf("RiverSoftStopTimeout = %v, want %v", cfg.Lifecycle.RiverSoftStopTimeout, defaultRiverSoftStopTimeout)
	}
	if cfg.Database.RequireExternalDB {
		t.Errorf("RequireExternalDB = true, want false")
	}
	if cfg.Database.URL != "" {
		t.Errorf("Database.URL = %q, want empty", cfg.Database.URL)
	}
	if cfg.ServerURL != "http://127.0.0.1:25678" {
		t.Errorf("ServerURL = %q, want default", cfg.ServerURL)
	}
}

func TestLoadServerConfigHappy(t *testing.T) {
	cfg, err := LoadServerConfig(lookupFrom(map[string]string{
		requireExternalDBEnv:    "true",
		databaseURLEnv:          "postgres://user:pass@db:5432/stella",
		httpShutdownTimeoutEnv:  "45s",
		riverSoftStopTimeoutEnv: "3m",
		serverURLEnv:            "http://stella.internal:9000",
	}))
	if err != nil {
		t.Fatalf("LoadServerConfig() unexpected error: %v", err)
	}
	if !cfg.Database.RequireExternalDB {
		t.Errorf("RequireExternalDB = false, want true")
	}
	if cfg.Database.URL != "postgres://user:pass@db:5432/stella" {
		t.Errorf("Database.URL = %q", cfg.Database.URL)
	}
	if cfg.Lifecycle.HTTPShutdownTimeout != 45*time.Second {
		t.Errorf("HTTPShutdownTimeout = %v, want 45s", cfg.Lifecycle.HTTPShutdownTimeout)
	}
	if cfg.Lifecycle.RiverSoftStopTimeout != 3*time.Minute {
		t.Errorf("RiverSoftStopTimeout = %v, want 3m", cfg.Lifecycle.RiverSoftStopTimeout)
	}
	if cfg.ServerURL != "http://stella.internal:9000" {
		t.Errorf("ServerURL = %q", cfg.ServerURL)
	}
}

// TestLoadServerConfigDurationQuadrants exercises the four unset/empty/
// whitespace/malformed states plus the >0 bound for a duration field.
func TestLoadServerConfigDurationQuadrants(t *testing.T) {
	cases := []struct {
		name    string
		set     bool
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", set: false, want: defaultHTTPShutdownTimeout},
		{name: "empty uses default", set: true, value: "", want: defaultHTTPShutdownTimeout},
		{name: "whitespace uses default", set: true, value: "   ", want: defaultHTTPShutdownTimeout},
		{name: "malformed rejected", set: true, value: "nope", wantErr: true},
		{name: "bare number rejected", set: true, value: "60", wantErr: true},
		{name: "zero rejected", set: true, value: "0s", wantErr: true},
		{name: "negative rejected", set: true, value: "-5s", wantErr: true},
		{name: "valid override", set: true, value: "90s", want: 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			if tc.set {
				m[httpShutdownTimeoutEnv] = tc.value
			}
			cfg, err := LoadServerConfig(lookupFrom(m))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadServerConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadServerConfig() unexpected error: %v", err)
			}
			if cfg.Lifecycle.HTTPShutdownTimeout != tc.want {
				t.Errorf("HTTPShutdownTimeout = %v, want %v", cfg.Lifecycle.HTTPShutdownTimeout, tc.want)
			}
		})
	}
}

// TestLoadServerConfigBoolQuadrants exercises the four states for the boolean
// guard.
func TestLoadServerConfigBoolQuadrants(t *testing.T) {
	cases := []struct {
		name    string
		set     bool
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset is false", set: false, want: false},
		{name: "empty is false", set: true, value: "", want: false},
		{name: "whitespace is false", set: true, value: "  ", want: false},
		{name: "malformed rejected", set: true, value: "yes", wantErr: true},
		{name: "one is true", set: true, value: "1", want: true},
		{name: "true is true", set: true, value: "true", want: true},
		{name: "zero is false", set: true, value: "0", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			if tc.set {
				m[requireExternalDBEnv] = tc.value
			}
			cfg, err := LoadServerConfig(lookupFrom(m))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadServerConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadServerConfig() unexpected error: %v", err)
			}
			if cfg.Database.RequireExternalDB != tc.want {
				t.Errorf("RequireExternalDB = %v, want %v", cfg.Database.RequireExternalDB, tc.want)
			}
		})
	}
}

func TestLoadServerConfigDurationMessage(t *testing.T) {
	_, err := LoadServerConfig(lookupFrom(map[string]string{httpShutdownTimeoutEnv: "nope"}))
	if err == nil {
		t.Fatal("expected error")
	}
	want := `STELLA_HTTP_SHUTDOWN_TIMEOUT="nope" is not a valid duration: use a Go duration such as 60s, 2m, or 500ms`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func TestLoadServerConfigBoolMessage(t *testing.T) {
	_, err := LoadServerConfig(lookupFrom(map[string]string{requireExternalDBEnv: "maybe"}))
	if err == nil {
		t.Fatal("expected error")
	}
	want := `STELLA_REQUIRE_EXTERNAL_DB="maybe" is not a boolean: set it to 1/true or 0/false`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// TestLoadServerConfigAggregatesErrors verifies every bad field is reported at
// once (via env.AggregateError), not one per restart.
func TestLoadServerConfigAggregatesErrors(t *testing.T) {
	_, err := LoadServerConfig(lookupFrom(map[string]string{
		requireExternalDBEnv:    "maybe",
		httpShutdownTimeoutEnv:  "nope",
		riverSoftStopTimeoutEnv: "later",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	var agg env.AggregateError
	if !errors.As(err, &agg) {
		t.Fatalf("error is not env.AggregateError: %T", err)
	}
	if len(agg.Errors) != 3 {
		t.Fatalf("aggregate has %d errors, want 3: %v", len(agg.Errors), agg.Errors)
	}
	for _, name := range []string{requireExternalDBEnv, httpShutdownTimeoutEnv, riverSoftStopTimeoutEnv} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("aggregated error %q missing field %q", err.Error(), name)
		}
	}
}

// TestLoadServerConfigNoSecretInError guards that a DSN with credentials never
// leaks into error text when another field fails to parse.
func TestLoadServerConfigNoSecretInError(t *testing.T) {
	const secret = "sup3rsecret"
	_, err := LoadServerConfig(lookupFrom(map[string]string{
		databaseURLEnv:         "postgres://admin:" + secret + "@db:5432/stella",
		httpShutdownTimeoutEnv: "nope",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error text leaked DSN credentials: %q", err.Error())
	}
}

func TestServerConfigWriteOnce(t *testing.T) {
	resetServerConfigForTest(t)

	cfg := ServerConfig{ServerURL: "http://once"}
	if err := InstallServerConfig(cfg); err != nil {
		t.Fatalf("first InstallServerConfig() error: %v", err)
	}
	if err := InstallServerConfig(cfg); err == nil {
		t.Fatal("second InstallServerConfig() error = nil, want error")
	}
	if got := InstalledServerConfig(); got.ServerURL != "http://once" {
		t.Errorf("InstalledServerConfig().ServerURL = %q, want installed value", got.ServerURL)
	}
}

func TestInstalledServerConfigPanicsBeforeInstall(t *testing.T) {
	resetServerConfigForTest(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("InstalledServerConfig() did not panic before install")
		}
	}()
	_ = InstalledServerConfig()
}

// resetServerConfigForTest clears the process-wide snapshot so a test can
// exercise the write-once install path from a clean slate. Test-only: production
// never resets the installed config. Tests using it must not call t.Parallel,
// since they mutate shared global state; the cleanup restores the empty state.
func resetServerConfigForTest(t *testing.T) {
	t.Helper()
	serverConfigMu.Lock()
	serverConfigInstalled = false
	serverConfigValue = ServerConfig{}
	serverConfigMu.Unlock()
	t.Cleanup(func() {
		serverConfigMu.Lock()
		serverConfigInstalled = false
		serverConfigValue = ServerConfig{}
		serverConfigMu.Unlock()
	})
}
