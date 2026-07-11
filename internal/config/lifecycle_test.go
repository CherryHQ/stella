package config

import (
	"testing"
	"time"
)

func TestParseDurationEnv(t *testing.T) {
	const env = "STELLA_TEST_DURATION"
	cases := []struct {
		name    string
		value   string
		set     bool
		def     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", set: false, def: 60 * time.Second, want: 60 * time.Second},
		{name: "empty uses default", value: "", set: true, def: 60 * time.Second, want: 60 * time.Second},
		{name: "whitespace uses default", value: "   ", set: true, def: 90 * time.Second, want: 90 * time.Second},
		{name: "seconds", value: "30s", set: true, def: 60 * time.Second, want: 30 * time.Second},
		{name: "minutes", value: "2m", set: true, def: 60 * time.Second, want: 2 * time.Minute},
		{name: "millis", value: "500ms", set: true, def: time.Second, want: 500 * time.Millisecond},
		{name: "zero rejected", value: "0s", set: true, wantErr: true},
		{name: "negative rejected", value: "-5s", set: true, wantErr: true},
		{name: "garbage rejected", value: "soon", set: true, wantErr: true},
		{name: "bare number rejected", value: "60", set: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(env, tc.value)
			} else {
				t.Setenv(env, "")
			}
			got, err := parseDurationEnv(env, tc.def)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDurationEnv() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDurationEnv() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseDurationEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLifecycleDefaults(t *testing.T) {
	t.Setenv(httpShutdownTimeoutEnv, "")
	t.Setenv(riverSoftStopTimeoutEnv, "")

	lc, err := LoadLifecycle()
	if err != nil {
		t.Fatalf("LoadLifecycle() unexpected error: %v", err)
	}
	if lc.HTTPShutdownTimeout != defaultHTTPShutdownTimeout {
		t.Errorf("HTTPShutdownTimeout = %v, want %v", lc.HTTPShutdownTimeout, defaultHTTPShutdownTimeout)
	}
	if lc.RiverSoftStopTimeout != defaultRiverSoftStopTimeout {
		t.Errorf("RiverSoftStopTimeout = %v, want %v", lc.RiverSoftStopTimeout, defaultRiverSoftStopTimeout)
	}
}

func TestLifecycleParsesOverrides(t *testing.T) {
	t.Setenv(httpShutdownTimeoutEnv, "45s")
	t.Setenv(riverSoftStopTimeoutEnv, "3m")

	lc, err := LoadLifecycle()
	if err != nil {
		t.Fatalf("LoadLifecycle() unexpected error: %v", err)
	}
	if lc.HTTPShutdownTimeout != 45*time.Second {
		t.Errorf("HTTPShutdownTimeout = %v, want 45s", lc.HTTPShutdownTimeout)
	}
	if lc.RiverSoftStopTimeout != 3*time.Minute {
		t.Errorf("RiverSoftStopTimeout = %v, want 3m", lc.RiverSoftStopTimeout)
	}
}

func TestLifecycleFailsFast(t *testing.T) {
	cases := []struct {
		name  string
		http  string
		river string
	}{
		{name: "bad http", http: "nope"},
		{name: "zero http", http: "0s"},
		{name: "bad river", river: "later"},
		{name: "zero river", river: "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(httpShutdownTimeoutEnv, tc.http)
			t.Setenv(riverSoftStopTimeoutEnv, tc.river)
			if _, err := LoadLifecycle(); err == nil {
				t.Fatalf("LoadLifecycle() error = nil, want error")
			}
		})
	}
}
