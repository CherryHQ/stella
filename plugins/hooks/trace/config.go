package trace

import "os"

// config holds OTel exporter settings, populated from standard OTel env vars.
type config struct {
	Endpoint    string  // OTLP gRPC endpoint
	ServiceName string  // OTel service name
	Insecure    bool    // use insecure gRPC connection
	SampleRate  float64 // trace sampling rate [0.0, 1.0]
}

func loadConfig() config {
	cfg := config{
		Endpoint:    "localhost:4317",
		ServiceName: "anna",
		Insecure:    true,
		SampleRate:  1.0,
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "false" {
		cfg.Insecure = false
	}
	return cfg
}
