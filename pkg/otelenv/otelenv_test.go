package otelenv

import "testing"

// clearEnv unsets every variable the predicate reads, so a case states its own
// world rather than inheriting the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_SDK_DISABLED",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_TRACES_EXPORTER",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		"OTEL_LOGS_EXPORTER",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_METRICS_EXPORTER",
	} {
		t.Setenv(k, "")
	}
}

func TestTracesEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "nothing configured", want: false},
		{
			name: "generic endpoint",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"},
			want: true,
		},
		{
			// The case that used to split the two implementations: a
			// traces-specific endpoint enabled the provider but not the
			// transport, so parent spans arrived with no children.
			name: "traces-specific endpoint",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://localhost:4318/v1/traces"},
			want: true,
		},
		{
			name: "explicit exporter without an endpoint",
			env:  map[string]string{"OTEL_TRACES_EXPORTER": "console"},
			want: true,
		},
		{
			// The other direction: export is off, so nothing should pay for spans.
			name: "exporter none overrides an endpoint",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
				"OTEL_TRACES_EXPORTER":        "none",
			},
			want: false,
		},
		{
			name: "sdk disabled overrides everything",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://localhost:4317",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://localhost:4318/v1/traces",
				"OTEL_TRACES_EXPORTER":               "console",
				"OTEL_SDK_DISABLED":                  "true",
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := TracesEnabled(); got != tc.want {
				t.Errorf("TracesEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Each signal reads its own variables: turning one off must not touch another.
func TestSignalsAreIndependent(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")

	if TracesEnabled() {
		t.Error("TracesEnabled() = true, want false when OTEL_TRACES_EXPORTER=none")
	}
	if !LogsEnabled() || !MetricsEnabled() {
		t.Error("silencing traces also silenced logs or metrics")
	}
}
