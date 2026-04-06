package trace

import "os"

// config holds OTel settings. Exporter-level env vars (OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_EXPORTER_OTLP_INSECURE) are consumed natively by the gRPC exporter SDK.
// Endpoint must be a standard URL with scheme (e.g. http://localhost:4317).
type config struct {
	Enabled     bool    // true when OTEL_EXPORTER_OTLP_ENDPOINT is set
	ServiceName string  // OTel service name
	SampleRate  float64 // trace sampling rate [0.0, 1.0]
}

func loadConfig() config {
	cfg := config{
		Enabled:     os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "",
		ServiceName: "anna",
		SampleRate:  1.0,
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	return cfg
}
